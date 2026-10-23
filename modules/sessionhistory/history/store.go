// Package history owns serialized publication of retained session records.
package history

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
)

// Store is the process-local view of one process-independent publication boundary.
// Copies of feature state share this owner rather than their own revision caches.
type Store struct {
	directory string
	service   *Service
	mu        sync.Mutex
	revision  string
	files     *storageFiles
}

// Sizes and their derived totals are one revision-validated publication view.
type storageFiles struct {
	sizes  map[string]int64
	kinds  map[string]int64
	shared int64
}

func (f *storageFiles) set(name string, kind recordkind.Spec, size int64) {
	delta := size - f.sizes[name]
	f.sizes[name] = size
	if !kind.SeparateBudget {
		f.shared += delta
	}
	if kind.ID != "" {
		f.kinds[kind.ID] += delta
	}
}

func (f *storageFiles) usage(kind recordkind.Spec) storageUsage {
	usage := storageUsage{kind: kind, total: f.shared, own: f.kinds[kind.ID]}
	if kind.SeparateBudget {
		usage.total = usage.own
	}
	return usage
}

// Directory is the selected retained path shared by this publication handle.
func (s *Store) Directory() string { return s.directory }

// Transaction is valid only during its Store.Transaction callback. It is never
// retained in records or held across delivery. Participants cannot open nested
// transactions through this handle.
type Transaction struct {
	store  *Store
	writes *persistence.Counter
	active bool
}

// Transaction serializes one operation across processes and publishes in caller
// order. A failure can leave completed writes; this is not crash-atomic storage.
// Source: internal/router/mekugi_store.go:134:162@efcbf71f locked.
func (s *Store) Transaction(ctx context.Context, writes *persistence.Counter, fn func(*Transaction) error) (err error) {
	path := filepath.Join(s.directory, "store.lock")
	info, e := os.Lstat(path)
	if e == nil && !info.Mode().IsRegular() {
		return errors.New("replay lock is not a regular file")
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	lock := flock.New(path, flock.SetPermissions(0600))
	ok, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return ctx.Err()
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	tx := &Transaction{store: s, writes: writes, active: true}
	defer func() { tx.active = false }()
	return fn(tx)
}

func (tx *Transaction) check() error {
	if tx == nil || !tx.active {
		return errors.New("publication transaction is no longer active")
	}
	return nil
}

// Revision identifies the complete serialized publication view.
// Source: internal/router/storage_prune.go:38:55@efcbf71f storageRevision.
func (tx *Transaction) Revision() (string, error) {
	if err := tx.check(); err != nil {
		return "", err
	}
	path := filepath.Join(tx.store.directory, "storage-revision")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 64 {
		return "", errors.New("invalid storage revision file")
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

// AdvanceRevision invalidates plans before any managed effect, including removal.
// Source: internal/router/storage_prune.go:57:72@efcbf71f advanceStorageRevision.
func (tx *Transaction) AdvanceRevision() error {
	if err := tx.check(); err != nil {
		return err
	}
	s := tx.store
	s.files = nil
	if _, err := tx.Revision(); err != nil {
		return err
	}
	revision := rand.Text() + "\n"
	if err := persistence.AtomicFile(filepath.Join(s.directory, "storage-revision"), "revision-pending-", []byte(revision), tx.writes); err != nil {
		return err
	}
	s.revision = revision
	return nil
}

// Write publishes one prepared record. Equal bytes preserve the current revision.
// Source: internal/router/mekugi_store.go:360:407@efcbf71f writeFile.
func (tx *Transaction) Write(name, pattern string, data []byte) error {
	if err := tx.check(); err != nil {
		return err
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return errors.New("invalid managed publication name")
	}
	s := tx.store
	kind, managed, kindErr := s.service.Kind(name)
	if kindErr != nil {
		return kindErr
	}
	path := filepath.Join(s.directory, name)
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("managed record is not a regular file")
		}
		if info.Size() == int64(len(data)) {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			previous, readErr := io.ReadAll(io.LimitReader(file, int64(len(data))+1))
			if err := errors.Join(readErr, file.Close()); err != nil {
				return err
			}
			if bytes.Equal(previous, data) {
				return nil
			}
		}
	}
	revision, err := tx.Revision()
	if err != nil {
		return err
	}
	var files *storageFiles
	if revision == s.revision {
		files = s.files
	}
	if err := tx.AdvanceRevision(); err != nil {
		return err
	}
	if err := persistence.AtomicFile(path, pattern, data, tx.writes); err != nil {
		return err
	}
	if files != nil {
		if managed || CatalogName(name) {
			files.set(name, kind, int64(len(data)))
		}
		s.files = files
	}
	return nil
}

// FileSizes returns the managed view under this transaction; callers must not
// mutate it or retain it after the transaction ends.
// Source: internal/router/storage_retention.go:730:773@efcbf71f storageFileSizes and admissionFileSizes.
func (tx *Transaction) FileSizes() (map[string]int64, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	s := tx.store
	revision, err := tx.Revision()
	if err != nil {
		return nil, err
	}
	if s.files != nil && s.revision == revision {
		return s.files.sizes, nil
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	files := &storageFiles{sizes: make(map[string]int64), kinds: make(map[string]int64)}
	for _, entry := range entries {
		kind, managed, err := s.service.Kind(entry.Name())
		if err != nil {
			return nil, err
		}
		if !managed && !CatalogName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("session storage contains a non-regular managed record; cleanup refused")
		}
		files.set(entry.Name(), kind, info.Size())
	}
	s.revision, s.files = revision, files
	return files.sizes, nil
}
