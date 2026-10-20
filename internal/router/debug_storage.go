package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// This empty lease is also provenance: only bundles created by this owner have it.
const debugBundleLease = ".mekugi-debug-v1.lock"

func debugStorageDirectory() (string, error) {
	base, err := mekugiStateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "debug"), nil
}

func createDebugBundle() (string, func() error, error) {
	root, err := debugStorageDirectory()
	if err != nil {
		return "", nil, err
	}
	if err := ensurePrivateStateDirectory(root); err != nil {
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

func cleanupDebugBundles(ctx context.Context, now time.Time) error {
	root, err := debugStorageDirectory()
	if err != nil {
		return err
	}
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
		if !info.Mode().IsRegular() || !info.ModTime().Before(now.Add(-sessionRetention)) {
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
		if err == nil && info.ModTime().Before(now.Add(-sessionRetention)) {
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
