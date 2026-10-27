package execscope

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var errExecWalkIncomplete = errors.New("scope enumeration exceeds budget")

// execPathWalker shares an admission budget across roots. Directory entries are
// charged when read, so pending siblings also count toward retained work.
type execPathWalker struct {
	remaining int
	deadline  time.Time
}

func (w *execPathWalker) walk(root string, visit fs.WalkDirFunc) error {
	if w.remaining <= 0 || time.Now().After(w.deadline) {
		return errExecWalkIncomplete
	}
	w.remaining--
	info, err := os.Lstat(root)
	if err != nil {
		err = visit(root, nil, err)
	} else {
		err = w.entry(root, fs.FileInfoToDirEntry(info), visit)
	}
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

func (w *execPathWalker) entry(path string, entry fs.DirEntry, visit fs.WalkDirFunc) error {
	if time.Now().After(w.deadline) {
		return errExecWalkIncomplete
	}
	if err := visit(path, entry, nil); err != nil || !entry.IsDir() {
		if err == fs.SkipDir && entry.IsDir() {
			return nil
		}
		return err
	}
	if time.Now().After(w.deadline) {
		return errExecWalkIncomplete
	}
	children, err := w.readDir(path)
	if err != nil && !errors.Is(err, errExecWalkIncomplete) {
		if err := visit(path, entry, err); err != nil {
			if err != fs.SkipDir {
				return err
			}
			return nil
		}
	}
	for _, child := range children {
		if err := w.entry(filepath.Join(path, child.Name()), child, visit); err != nil {
			if err == fs.SkipDir {
				break
			}
			return err
		}
	}
	if errors.Is(err, errExecWalkIncomplete) {
		return err
	}
	return nil
}

func (w *execPathWalker) readDir(path string) ([]fs.DirEntry, error) {
	if time.Now().After(w.deadline) {
		return nil, errExecWalkIncomplete
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if time.Now().After(w.deadline) {
		directory.Close()
		return nil, errExecWalkIncomplete
	}
	// One extra entry distinguishes a complete batch from an overflowing one,
	// including an empty directory when the admission budget is exhausted.
	children, err := directory.ReadDir(w.remaining + 1)
	directory.Close()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if len(children) > w.remaining {
		children = children[:w.remaining]
		err = errExecWalkIncomplete
	}
	w.remaining -= len(children)
	slices.SortFunc(children, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return children, err
}

// glob follows filepath.Glob's component matching and symlinked-directory
// behavior, sharing directory-entry admission with subsequent formatter walks.
// Source: src/path/filepath/match.go:235:289@go1.27.3 globWithLimit
func (w *execPathWalker) glob(pattern string) ([]string, error) {
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if time.Now().After(w.deadline) {
		return nil, errExecWalkIncomplete
	}
	magic := "*?["
	if filepath.Separator != '\\' {
		magic += `\`
	}
	if !strings.ContainsAny(pattern, magic) {
		if _, err := os.Lstat(pattern); err != nil {
			return nil, nil
		}
		return []string{pattern}, nil
	}
	var components, directories []string
	for {
		directory, component := filepath.Split(pattern)
		directory = filepath.Clean(directory)
		if directory == pattern {
			return nil, filepath.ErrBadPattern
		}
		components = append(components, component)
		if !strings.ContainsAny(directory[len(filepath.VolumeName(directory)):], magic) {
			directories = []string{directory}
			break
		}
		if len(components) == 10_000 {
			return nil, filepath.ErrBadPattern
		}
		pattern = directory
	}
	for i := len(components) - 1; i >= 0; i-- {
		var matches []string
		for _, directory := range directories {
			if time.Now().After(w.deadline) {
				return nil, errExecWalkIncomplete
			}
			// Glob follows directory symlinks, whereas recursive formatter
			// walks retain WalkDir's nontraversal of symlinks.
			info, err := os.Stat(directory)
			if err != nil || !info.IsDir() {
				continue
			}
			children, err := w.readDir(directory)
			for _, child := range children {
				if time.Now().After(w.deadline) {
					err = errExecWalkIncomplete
					break
				}
				matched, matchErr := filepath.Match(components[i], child.Name())
				if matchErr != nil {
					return nil, matchErr
				}
				if matched {
					matches = append(matches, filepath.Join(directory, child.Name()))
				}
			}
			if errors.Is(err, errExecWalkIncomplete) {
				if i != 0 {
					return nil, err
				}
				return matches, err
			}
		}
		directories = matches
	}
	return directories, nil
}
