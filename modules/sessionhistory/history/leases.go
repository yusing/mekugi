package history

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/libraries/persistence"
)

// BeginSession resolves retained identity before leasing and enrolling its owner.
// The caller releases the lease after delivery or session retirement.
// Source: apps/mekugi/internal/router/storage_retention.go:49:74@[55257c48] beginSession.
func (s *Store) BeginSession(ctx context.Context, writes *persistence.Counter, limits Limits, thread string) (string, func(), error) {
	if s == nil || thread == "" {
		return thread, func() {}, nil
	}
	namespace := thread
	if err := s.Transaction(ctx, writes, func(*Transaction) error {
		var err error
		namespace, err = s.NamespaceForThread(thread)
		return err
	}); err != nil {
		return "", nil, err
	}
	release, err := s.SessionLease(ctx, thread)
	if err != nil {
		return "", nil, fmt.Errorf("acquire session storage lease: %w", err)
	}
	if err := s.Transaction(ctx, writes, func(tx *Transaction) error {
		if err := tx.Retain(thread, limits); err != nil {
			return &PublicationError{Err: err}
		}
		return nil
	}); err != nil {
		release()
		return "", nil, err
	}
	return namespace, release, nil
}

// SessionName preserves the existing durable ownership catalog identity.
func SessionName(thread string) string {
	return fmt.Sprintf("session-%x.json", sha256.Sum256([]byte(thread)))
}

// SessionLease protects active work until its caller confirms delivery or exits.
// Source: internal/router/storage_retention.go:76:88@efcbf71f beginSession.
func (s *Store) SessionLease(ctx context.Context, thread string) (func(), error) {
	return s.sharedLease(ctx, strings.TrimSuffix(SessionName(thread), ".json")+".lock", "session storage lease is not a regular file")
}

// SnapshotLease bridges reads and dependency adoption. Acquire it before any
// publication transaction; pruning only tries its exclusive side without waiting.
func (s *Store) SnapshotLease(ctx context.Context) (func(), error) {
	return s.sharedLease(ctx, "retention-snapshot.lock", "invalid retention snapshot lease")
}

func (s *Store) sharedLease(ctx context.Context, name, message string) (func(), error) {
	path := filepath.Join(s.directory, name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New(message)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lease := flock.New(path, flock.SetPermissions(0600))
	ok, err := lease.TryRLockContext(ctx, 25*time.Millisecond)
	if err != nil || !ok {
		return nil, errors.Join(err, ctx.Err())
	}
	return func() { _ = lease.Unlock() }, nil
}

// TryPruneLease never waits while publication is serialized. Lease inodes are
// stable and never removed, including after the last reader releases them.
// Source: internal/router/storage_prune.go:300:310@efcbf71f tryPruneLease.
func (s *Store) TryPruneLease(name string) (bool, func(), error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return false, nil, errors.New("invalid storage prune lease name")
	}
	path := filepath.Join(s.directory, name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return false, nil, errors.New("invalid storage prune lease")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, nil, err
	}
	lease := flock.New(path, flock.SetPermissions(0600))
	ok, err := lease.TryLock()
	return ok, func() { _ = lease.Unlock() }, err
}
