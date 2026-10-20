// Source: internal/router/debug_storage.go@[ac8511fa] bundle provenance and retention.
package diagnostic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/persistence"

	"github.com/gofrs/flock"
)

// This empty lease is also provenance: only bundles created by this owner have it.
const debugBundleLease = ".mekugi-debug-v1.lock"

func createBundle(root string) (string, func() error, error) {
	if err := persistence.EnsurePrivateDirectory(root); err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp(root, "mekugi-debug-")
	if err != nil {
		return "", nil, err
	}
	lease := flock.New(filepath.Join(directory, debugBundleLease), flock.SetPermissions(0600))
	if err := lease.Lock(); err != nil {
		return "", nil, err
	}
	return directory, func() error {
		// Inactivity starts on close, not launch. Active writers are protected by the lease.
		now := time.Now()
		return errors.Join(os.Chtimes(lease.Path(), now, now), lease.Unlock())
	}, nil
}

func (*Service) CleanupBundles(ctx context.Context, root string, now time.Time) error {
	resolved, err := filepath.EvalSymlinks(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if resolved != root {
		return errors.New("debug storage directory must not contain symlinks")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "mekugi-debug-") {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		path := filepath.Join(directory, debugBundleLease)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(now.Add(-(14 * 24 * time.Hour))) {
			continue
		}
		lease := flock.New(path, flock.SetPermissions(0600))
		locked, err := lease.TryLock()
		if err != nil {
			return err
		}
		if !locked {
			continue
		}
		// A writer may have closed between the first stat and acquiring its lease.
		info, err = os.Lstat(path)
		if err == nil && info.ModTime().Before(now.Add(-(14 * 24 * time.Hour))) {
			// RemoveAll never follows bundle symlinks, including custom capture destinations.
			err = os.RemoveAll(directory)
		}
		err = errors.Join(err, lease.Unlock())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
