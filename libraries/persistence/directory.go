package persistence

import (
	"errors"
	"os"
	"path/filepath"
)

// EnsurePrivateDirectory creates a private state directory without traversing symlinks.
// Source: internal/router/mekugi_store.go:411:441@efcbf71f ensurePrivateStateDirectory.
func EnsurePrivateDirectory(directory string) error {
	// Inspect existing ancestors before MkdirAll can create anything through a
	// symlink. The second check below also validates the completed path.
	for ancestor := directory; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("state directory must not contain symlinks")
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	if resolved != directory {
		return errors.New("state directory must not contain symlinks")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}

	return nil
}
