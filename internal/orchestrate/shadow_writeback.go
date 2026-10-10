package orchestrate

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ShadowWrite retains intent before replacement. An uncertain effect is only
// reconciled against its postimage, never automatically repeated.
type ShadowWrite struct {
	Path  string `json:"path"`
	State string `json:"state"`
	Temp  string `json:"temp,omitempty"`
}

type shadowEntry struct{ mode, object string }

// IntegrateShadow plans fresh work or continues retained writeback. Only the
// live coordinator can call it; the router reserves its idle child first.
func (s *Store) IntegrateShadow(ctx context.Context, workspace, main, name, thread string) (Batch, error) {
	batches, err := s.List(ctx, workspace, main)
	if err != nil {
		return Batch{}, err
	}
	index := slices.IndexFunc(batches, func(b Batch) bool { return b.TaskName == name })
	if index < 0 {
		return Batch{}, errors.New("shadow integration target is not in this run")
	}
	b := batches[index]
	if b.ShadowMerge == nil || b.ShadowMerge.State == "planned" || b.ShadowMerge.State == "conflicted" {
		b, err = s.PlanShadowMerge(ctx, workspace, main, name, thread)
		if err != nil || b.ShadowMerge.State == "conflicted" {
			return b, err
		}
	} else if b.ShadowMerge.State == "applied" {
		tip, err := sourceBase(ctx, b.Cwd)
		if err != nil {
			return b, err
		}
		if tip != b.ShadowMerge.Tip {
			b, err = s.PlanShadowMerge(ctx, workspace, main, name, thread)
			if err != nil || b.ShadowMerge.State == "conflicted" {
				return b, err
			}
		}
	}
	return s.applyShadowMerge(ctx, workspace, main, name, thread)
}

func (s *Store) applyShadowMerge(ctx context.Context, workspace, main, name, thread string) (batch Batch, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		index := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.TaskName == name })
		if index < 0 {
			return errors.New("shadow merge target is not in this run")
		}
		b := &m.Batches[index]
		defer func() { batch = *b }()
		if !shadowVCS(b.VCS) || b.State != "launched" || b.Launch == nil || b.Launch.ThreadID != thread || thread == "" || b.ShadowMerge == nil {
			return errors.New("writeback requires a confirmed shadow batch and merge plan")
		}
		for _, d := range m.Deliveries {
			if (d.Target == thread || d.From == thread) && (d.State == "queued" || d.State == "dispatching" || d.State == "uncertain") {
				return errors.New("shadow writeback requires settled batch deliveries")
			}
		}
		if err := validateCheckoutIdentity(ctx, *b); err != nil {
			return err
		}
		if err := validateIndexFlags(ctx, b.Checkout); err != nil {
			return err
		}
		status, err := git(ctx, b.Cwd, "status", "--porcelain", "--untracked-files=all")
		if err != nil || status != "" {
			return errors.New("shadow writeback requires a clean batch checkout")
		}
		tip, err := sourceBase(ctx, b.Cwd)
		if err != nil || tip != b.ShadowMerge.Tip {
			return errors.New("shadow batch tip changed during writeback")
		}
		vcs, source, err := sourceRepository(ctx, workspace)
		if err != nil || vcs != b.VCS || source != b.Source {
			return errors.New("shadow source identity changed")
		}
		root, err := os.OpenRoot(source)
		if err != nil {
			return err
		}
		defer root.Close()
		before, err := shadowEntries(ctx, b.Repository, b.ShadowMerge.SourceTip)
		if err != nil {
			return err
		}
		after, err := shadowEntries(ctx, b.Repository, b.ShadowMerge.Tree)
		if err != nil {
			return err
		}
		plan := b.ShadowMerge
		if plan.State != "planned" && plan.State != "applying" && plan.State != "applied" {
			return errors.New("shadow merge has no clean writeback plan")
		}
		if plan.State == "planned" {
			plan.Writes = nil
			// Remove leaves first so file/directory transitions do not remove
			// unplanned content. Empty directories are removed only as needed.
			for _, name := range plan.Paths {
				if after[name].mode == "" || after[name].mode == "040000" {
					plan.Writes = append(plan.Writes, ShadowWrite{Path: name, State: "pending"})
				}
			}
			for _, name := range plan.Paths {
				if after[name].mode != "" && after[name].mode != "040000" {
					plan.Writes = append(plan.Writes, ShadowWrite{Path: name, State: "pending"})
				}
			}
		}
		// Preflight the complete remaining set before any source effect.
		for _, w := range plan.Writes {
			if w.Temp != "" {
				if _, err := root.Lstat(w.Temp); !errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("%s: retained temporary path requires inspection", w.Temp)
				}
			}
			want := before[w.Path]
			parents := before
			if w.State == "applied" || w.State == "applying" {
				want = after[w.Path]
				parents = after
			} else if w.State != "pending" {
				return fmt.Errorf("%s: unknown shadow writeback state", w.Path)
			}
			if err := shadowMatches(ctx, root, b.Repository, w.Path, want, parents); err != nil {
				return fmt.Errorf("%s: source changed or uncertain write requires inspection: %w", w.Path, err)
			}
			info, statErr := root.Lstat(w.Path)
			if w.State == "pending" && statErr == nil && info.IsDir() && after[w.Path].mode != "040000" && after[w.Path].mode != "" {
				if err := fs.WalkDir(root.FS(), w.Path, func(name string, info fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !info.IsDir() && !slices.Contains(plan.Paths, name) {
						return fmt.Errorf("%s: unplanned directory content", name)
					}
					return nil
				}); err != nil {
					return err
				}
			}
		}
		if plan.State == "applied" {
			return nil
		}
		plan.State = "applying"
		b.Integration = nil
		if err := s.save(m, path); err != nil {
			return err
		}
		for i := range plan.Writes {
			w := &plan.Writes[i]
			if err := ctx.Err(); err != nil {
				return err
			}
			if w.State == "applied" {
				continue
			}
			if w.State == "pending" {
				if err := shadowMatches(ctx, root, b.Repository, w.Path, before[w.Path], before); err != nil {
					return fmt.Errorf("%s: source preimage changed: %w", w.Path, err)
				}
				w.State = "applying"
				if after[w.Path].mode != "" && after[w.Path].mode != "040000" {
					w.Temp = filepath.Join(filepath.Dir(w.Path), ".mekugi-orchestrate-"+rand.Text())
				}
				if err := s.save(m, path); err != nil {
					return err
				}
				if err := shadowReplace(ctx, root, b.Repository, *w, after[w.Path]); err != nil {
					return fmt.Errorf("%s: writeback incomplete: %w", w.Path, err)
				}
			}
			// An applying path from a previous call passed its postimage check.
			w.State = "applied"
			if err := s.save(m, path); err != nil {
				return err
			}
		}
		plan.State = "applied"
		b.Integration = &Integration{Tip: plan.Tip, SourceTip: plan.Tree}
		return s.save(m, path)
	})
	if err != nil {
		// Failed publication cannot turn attempted progress into retained proof.
		if batches, readErr := s.Snapshot(workspace, main); readErr == nil {
			index := slices.IndexFunc(batches, func(b Batch) bool { return b.TaskName == name })
			if index >= 0 {
				batch = batches[index]
			}
		} else {
			batch.ShadowMerge, batch.Integration = nil, nil
			err = errors.Join(err, fmt.Errorf("read retained writeback progress: %w", readErr))
		}
	}
	return batch, err
}

func shadowEntries(ctx context.Context, repository, tree string) (map[string]shadowEntry, error) {
	out, err := shadowGit(ctx, repository, "ls-tree", "-r", "-t", "-z", tree)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]shadowEntry)
	for _, row := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if row == "" {
			continue
		}
		header, name, ok := strings.Cut(row, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || !filepath.IsLocal(name) || filepath.ToSlash(filepath.Clean(name)) != name {
			return nil, errors.New("invalid shadow tree entry")
		}
		for _, component := range strings.Split(name, "/") {
			if slices.Contains([]string{".git", ".svn", ".hg", ".jj", ".bzr"}, component) {
				return nil, errors.New("shadow tree includes VCS metadata")
			}
		}
		if !slices.Contains([]string{"100644", "100755", "120000", "040000"}, fields[0]) {
			return nil, errors.New("unsupported shadow tree mode")
		}
		entries[name] = shadowEntry{fields[0], fields[2]}
	}
	return entries, nil
}

// Parent links are not aliases for planned paths. A file ancestor scheduled to
// become a directory makes a presently unreachable child absent, not writable.
func shadowMatches(ctx context.Context, root *os.Root, repository, name string, want shadowEntry, before map[string]shadowEntry) error {
	components := strings.Split(filepath.ToSlash(name), "/")
	for i := 1; i < len(components); i++ {
		dir := filepath.Join(components[:i]...)
		info, err := root.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if want.mode == "" && before[dir].mode != "" && before[dir].mode != "040000" {
				return nil
			}
			return errors.New("source parent is not a directory")
		}
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if want.mode == "" || want.mode == "040000" {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	if want.mode == "" || want.mode == "040000" {
		if info.IsDir() {
			return nil // Empty-directory checks happen before replacement.
		}
		return errors.New("expected absent file or directory")
	}
	var data []byte
	if want.mode == "120000" && info.Mode()&os.ModeSymlink != 0 {
		link, err := root.Readlink(name)
		if err != nil {
			return err
		}
		data = []byte(link)
	} else if info.Mode().IsRegular() && (want.mode == "100644" || want.mode == "100755") {
		if (info.Mode().Perm()&0111 != 0) != (want.mode == "100755") {
			return errors.New("source executable state changed")
		}
		data, err = root.ReadFile(name)
		if err != nil {
			return err
		}
	} else {
		return errors.New("source file type changed")
	}
	expected, err := shadowGit(ctx, repository, "cat-file", "blob", want.object)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return errors.New("source bytes changed")
	}
	return nil
}

func shadowReplace(ctx context.Context, root *os.Root, repository string, w ShadowWrite, entry shadowEntry) error {
	if entry.mode == "" || entry.mode == "040000" {
		if err := root.Remove(w.Path); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := shadowGit(ctx, repository, "cat-file", "blob", entry.object)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(w.Path), 0755); err != nil {
		return err
	}
	if entry.mode == "120000" {
		err = root.Symlink(string(data), w.Temp)
	} else {
		mode := os.FileMode(0644)
		if info, e := root.Lstat(w.Path); e == nil && info.Mode().IsRegular() {
			mode = info.Mode().Perm() &^ 0111
		}
		if entry.mode == "100755" {
			mode |= (mode & 0444) >> 2
		}
		f, e := root.OpenFile(w.Temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if e != nil {
			return e
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Chmod(mode) // The recovering process can have a different umask.
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			root.Remove(w.Temp)
			return err
		}
	}
	if err != nil {
		return err
	}
	defer root.Remove(w.Temp) // This invocation created the exact temporary path.
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := root.Lstat(w.Path); err == nil && info.IsDir() {
		// Only empty directories can remain after planned leaf deletions.
		var dirs []string
		if err := fs.WalkDir(root.FS(), w.Path, func(name string, info fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("%s: unplanned directory content", name)
			}
			dirs = append(dirs, name)
			return nil
		}); err != nil {
			return err
		}
		slices.Reverse(dirs)
		for _, dir := range dirs {
			if err := root.Remove(dir); err != nil {
				return err
			}
		}
	}
	return root.Rename(w.Temp, w.Path)
}

func verifyShadowIntegration(ctx context.Context, b Batch) (Integration, error) {
	plan := b.ShadowMerge
	if plan == nil || plan.State != "applied" || b.Integration == nil || *b.Integration != (Integration{Tip: plan.Tip, SourceTip: plan.Tree}) {
		return Integration{}, errors.New("shadow writeback is not confirmed")
	}
	tip, err := sourceBase(ctx, b.Cwd)
	if err != nil || tip != plan.Tip {
		return Integration{}, errors.New("shadow batch tip changed")
	}
	root, err := os.OpenRoot(b.Source)
	if err != nil {
		return Integration{}, err
	}
	defer root.Close()
	after, err := shadowEntries(ctx, b.Repository, plan.Tree)
	if err != nil {
		return Integration{}, err
	}
	for _, name := range plan.Paths {
		if err := shadowMatches(ctx, root, b.Repository, name, after[name], after); err != nil {
			return Integration{}, fmt.Errorf("%s: integrated source changed: %w", name, err)
		}
	}
	return *b.Integration, nil
}
