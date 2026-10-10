package orchestrate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/fileidentity"
)

type EvidenceInput struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type Evidence struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	State    string `json:"state"`
	SHA256   string `json:"sha256,omitempty"`
	Identity string `json:"identity,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RetainEvidence publishes each copy's intent before creating its exact path.
// A repeat uses the original copy, never the current source or an uncertain effect.
func (s *Store) RetainEvidence(ctx context.Context, workspace, main, name string, inputs []EvidenceInput) (batch Batch, err error) {
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if input.Name == "" || input.Name == "." || input.Name == ".." || strings.ContainsAny(input.Name, "/\\\x00") || !filepath.IsAbs(input.Source) || seen[input.Name] {
			return batch, errors.New("evidence requires unique filenames and absolute source paths")
		}
		seen[input.Name] = true
	}
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Batches {
			b := &m.Batches[i]
			if b.TaskName != name {
				continue
			}
			if b.State != "prepared" || b.Launch != nil {
				return errors.New("evidence requires an unlaunched prepared batch")
			}
			var failures error
			for _, input := range inputs {
				var retained *Evidence
				for j := range b.Evidence {
					if b.Evidence[j].Name == input.Name {
						retained = &b.Evidence[j]
						break
					}
				}
				if retained != nil {
					if retained.Source != input.Source {
						failures = errors.Join(failures, fmt.Errorf("evidence %q already has another source", input.Name))
					} else {
						failures = errors.Join(failures, validateEvidence(*retained))
					}
					continue
				}
				directory, relative := filepath.Dir(b.Checkout), filepath.Join(".evidence", name, input.Name)
				destination := filepath.Join(directory, relative)
				b.Evidence = append(b.Evidence, Evidence{Name: input.Name, Source: input.Source, Path: destination, State: "copying"})
				if err := s.save(m, path); err != nil {
					return err
				}
				e := &b.Evidence[len(b.Evidence)-1]
				e.SHA256, e.Identity, err = copyEvidence(ctx, input.Source, directory, relative)
				if err != nil {
					e.State, e.Error = "failed", err.Error()
					failures = errors.Join(failures, fmt.Errorf("evidence %q: %w", input.Name, err))
				} else {
					e.State = "ready"
				}
				if err := s.save(m, path); err != nil {
					return errors.Join(failures, err)
				}
			}
			batch = *b
			return failures
		}
		return errors.New("batch is not prepared")
	})
	return batch, err
}

func copyEvidence(ctx context.Context, source, directory, relative string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("source is not a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", "", err
	}
	defer input.Close()
	info, err = input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("source is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return "", "", errors.New("run storage is unavailable or redirected")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(relative), 0700); err != nil {
		return "", "", err
	}
	output, err := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	info, statErr := output.Stat()
	err = errors.Join(copyErr, statErr, output.Sync(), output.Close(), ctx.Err())
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), fileidentity.FromInfo(info), nil
}

func validateEvidence(e Evidence) error {
	if e.State != "ready" {
		return fmt.Errorf("evidence %q is %s; inspect its retained copy before recovery", e.Name, e.State)
	}
	info, err := os.Lstat(e.Path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("evidence %q retained file is unavailable", e.Name)
	}
	resolved, err := filepath.EvalSymlinks(e.Path)
	if err != nil || resolved != e.Path {
		return fmt.Errorf("evidence %q retained path was redirected", e.Name)
	}
	file, err := os.Open(e.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("evidence %q retained bytes changed", e.Name)
	}
	return nil
}

// cleanupEvidence runs only after the accepted checkout's removal is confirmed.
// It removes exact files, never directories or uncertain surviving copies.
func (s *Store) cleanupEvidence(ctx context.Context, m *manifest, path string, b *Batch) error {
	if len(b.Evidence) == 0 {
		return nil
	}
	directory := filepath.Join(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), ".json"))
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return errors.New("evidence run storage is unavailable or redirected")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	var failures error
	for i := range b.Evidence {
		e := &b.Evidence[i]
		previous := *e
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		relative, err := evidenceCleanupPath(root, directory, e, b, m)
		if err == nil {
			info, statErr := root.Lstat(relative)
			switch {
			case e.State == "removing" || e.State == "removed":
				if !errors.Is(statErr, os.ErrNotExist) {
					err = errors.New("copy survives or was recreated; inspect removal before recovery")
				} else {
					e.State = "removed"
				}
			case e.State != "ready":
				err = fmt.Errorf("copy is %s; inspect before cleanup", e.State)
			case statErr != nil:
				err = statErr
			case !info.Mode().IsRegular():
				err = errors.New("copy is not a regular file")
			case e.Identity == "" || fileidentity.FromInfo(info) != e.Identity:
				err = errors.New("copy identity is missing or changed")
			default:
				err = validateEvidence(*e)
				if err == nil {
					e.State, e.Error = "removing", ""
					if err := s.save(m, path); err != nil {
						return errors.Join(failures, err)
					}
					// Recheck after publishing intent so changed files remain intact.
					check := *e
					check.State = "ready"
					err = ctx.Err()
					if err == nil {
						err = validateEvidence(check)
					}
					if err == nil {
						current, statErr := root.Lstat(relative)
						if statErr != nil || !os.SameFile(info, current) {
							err = errors.New("copy identity changed during cleanup")
						} else {
							err = root.Remove(relative)
						}
					}
					if err == nil {
						e.State = "removed"
					}
				}
			}
		}
		e.Error = ""
		if err != nil {
			e.Error = err.Error()
			failures = errors.Join(failures, fmt.Errorf("evidence %q: %w", e.Name, err))
		}
		if *e != previous {
			if err := s.save(m, path); err != nil {
				return errors.Join(failures, err)
			}
		}
	}
	return failures
}

func evidenceCleanupPath(root *os.Root, directory string, e *Evidence, b *Batch, m *manifest) (string, error) {
	if e.Name == "" || e.Name == "." || e.Name == ".." || strings.ContainsAny(e.Name, "/\\\x00") || !taskName.MatchString(b.TaskName) {
		return "", errors.New("invalid retained evidence name")
	}
	relative := filepath.Join(".evidence", b.TaskName, e.Name)
	if e.Path != filepath.Join(directory, relative) {
		return "", errors.New("copy is outside its recorded run-owned location")
	}
	for _, parent := range []string{".evidence", filepath.Dir(relative)} {
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil || !info.IsDir() {
			return "", errors.New("evidence directory is unavailable or redirected")
		}
	}
	info, _ := root.Lstat(relative)
	for _, batch := range m.Batches {
		for j := range batch.Evidence {
			other := &batch.Evidence[j]
			if other == e {
				continue
			}
			otherInfo, _ := os.Stat(other.Path)
			if other.Path == e.Path || info != nil && otherInfo != nil && os.SameFile(info, otherInfo) {
				return "", errors.New("copy is shared by retained evidence")
			}
		}
	}
	return relative, nil
}
