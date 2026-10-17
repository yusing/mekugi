package history

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/libraries/persistence"
)

// StateDirectory resolves product-owned state without creating it.
// Source: internal/router/mekugi_store.go:94:107@8bcb5cbf mekugiStateDirectory
func StateDirectory() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		userDirectory, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(userDirectory, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(base, "mekugi"), nil
}

// Source: apps/mekugi/internal/router/mekugi_store.go:75:82@45b7fe8d
// ReplayDirectory resolves the invocation default for retained session records.
func ReplayDirectory() (string, error) {
	base, err := StateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "replay"), nil
}

// PublishFile atomically replaces an independent, owner-encoded private file.
// It does not participate in store transactions, revisions, catalogs or retention.
// Directory links retain their existing behavior; a final link is replaced.
func (s *Service) PublishFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return persistence.AtomicFile(path, "record-pending-", data, nil)
}
