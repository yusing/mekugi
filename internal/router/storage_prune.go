package router

import (
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/yusing/mekugi/internal/persistence"
)

const storagePruneBatchFiles = 16

var errStoragePruneStale = errors.New("storage changed during background prune planning")
var errStoragePruneBusy = errors.New("inherited snapshot validation is active")

type storagePressureRequest struct {
	Name   string `json:"name"`
	Growth int64  `json:"growth"`
}

// This revision is coordination metadata, not retained evidence. Every managed
// publication changes it under store.lock before mutating files. A planner can
// then do all directory/catalog/index reads outside that lock and reject stale
// ownership before any removal, including across router processes.
func (s *mekugiReplayStore) storageRevision() (string, error) {
	path := filepath.Join(s.directory, "storage-revision")
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
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *mekugiReplayStore) advanceStorageRevision() error {
	if s.admission != nil {
		s.admission.files = nil
	}
	if _, err := s.storageRevision(); err != nil {
		return err
	}
	revision := rand.Text() + "\n"
	if err := persistence.AtomicFile(filepath.Join(s.directory, "storage-revision"), "revision-pending-", []byte(revision), s.writes); err != nil {
		return err
	}
	if s.admission != nil {
		s.admission.revision = revision
	}
	return nil
}

func (s *mekugiReplayStore) requestStoragePrune(request storagePressureRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	// One coalesced store-wide request contains only a managed name and byte
	// growth, never request views, tool inputs, thread ancestry or process handles.
	return s.writeFile("retention-pressure", "pressure-pending-", data)
}

func (s *mekugiReplayStore) readStoragePressure() (*storagePressureRequest, error) {
	path := filepath.Join(s.directory, "retention-pressure")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("invalid storage pressure request file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var request storagePressureRequest
	if err := json.Unmarshal(data, &request, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if (request.Name != "" && !retainedDataName(request.Name) && !storageCatalogName(request.Name)) || request.Growth < 0 || request.Growth > 2*maxReplayRecordBytes {
		return nil, errors.New("invalid storage pressure request")
	}
	return &request, nil
}

type storagePrunePlan struct {
	revision string
	snapshot storageSnapshot
	indexes  map[string]changeIndex
	pressure *storagePressureRequest
	expire   bool
}

func (s *mekugiReplayStore) planStoragePrune(ctx context.Context) (*storagePrunePlan, error) {
	plan := &storagePrunePlan{indexes: make(map[string]changeIndex)}
	err := s.locked(ctx, func() error {
		var err error
		plan.revision, err = s.storageRevision()
		if err != nil {
			return err
		}
		plan.pressure, err = s.readStoragePressure()
		if err != nil {
			return err
		}
		info, err := os.Lstat(filepath.Join(s.directory, "retention-sweep"))
		if err == nil && !info.Mode().IsRegular() {
			return errors.New("invalid session retention sweep marker")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		plan.expire = info == nil || time.Since(info.ModTime()) >= time.Hour
		return nil
	})
	if err != nil || !plan.expire && plan.pressure == nil {
		return nil, err
	}
	// Large reads and decoding are deliberately outside store.lock. Concurrent
	// publishers remain free to start sessions; the revision validates this view.
	plan.snapshot, err = s.storageSnapshotContext(ctx)
	if err == nil {
		for _, name := range slices.Sorted(maps.Keys(plan.snapshot.files)) {
			if err = ctx.Err(); err != nil {
				break
			}
			if !strings.HasPrefix(name, changeIndexPrefix) {
				continue
			}
			var data []byte
			data, err = readManagedOutputFile(filepath.Join(s.directory, name))
			if err != nil {
				break
			}
			var index changeIndex
			if err = jsonv1.Unmarshal(data, &index); err != nil {
				break
			}
			if changeIndexName(index.Workspace, index.Namespace) != name {
				err = errors.New("invalid background change index identity")
				break
			}
			if err = validateChangeIndex(index); err != nil {
				break
			}
			plan.indexes[name] = index
		}
	}
	readErr := err
	err = s.locked(ctx, func() error {
		revision, err := s.storageRevision()
		if err != nil {
			return err
		}
		if revision != plan.revision {
			return errStoragePruneStale
		}
		return readErr
	})
	return plan, err
}

func (s *mekugiReplayStore) pruneRequired(plan *storagePrunePlan) bool {
	name, size := "", int64(0)
	if plan.pressure != nil {
		name = plan.pressure.Name
		size = plan.snapshot.files[name] + plan.pressure.Growth
	}
	required, limit := s.storageNeeds(plan.snapshot.files, name, size)
	return required > limit
}

// One commit has a bounded file batch. All decoding, composition and encoding
// happen before acquiring store.lock; leases and revision are checked again
// immediately before effects. No lock is held between batches.
func (s *mekugiReplayStore) commitStoragePrune(ctx context.Context, plan *storagePrunePlan, candidate *storageCandidate) (bool, int64, error) {
	if candidate.pending == nil {
		candidate.pending = slices.Sorted(maps.Keys(candidate.files))
	}
	names := candidate.pending
	names = names[:min(len(names), storagePruneBatchFiles)]
	deleted := make(map[string]bool)
	for _, name := range names {
		if plan.snapshot.owners[name] <= 1 && !strings.HasPrefix(name, changeIndexPrefix) {
			deleted[name] = true
		}
	}
	indexes := make(map[string]changeIndex)
	encoded := make(map[string][]byte)
	for name, original := range plan.indexes {
		if err := ctx.Err(); err != nil {
			return false, 0, err
		}
		index := original
		if retireStoredChanges(&index, deleted, candidate.thread) {
			data, err := marshalProtocolJSON(index)
			if err != nil {
				return false, 0, err
			}
			indexes[name], encoded[name] = index, data
		}
	}
	committed, freed := false, int64(0)
	err := s.locked(ctx, func() error {
		revision, err := s.storageRevision()
		if err != nil {
			return err
		}
		if revision != plan.revision {
			return errStoragePruneStale
		}
		snapshotLease, release, err := s.tryPruneLease("retention-snapshot.lock")
		if err != nil {
			return err
		}
		if !snapshotLease {
			return errStoragePruneBusy
		}
		defer release()
		if candidate.thread != "" {
			leased, release, err := s.tryPruneLease(strings.TrimSuffix(storageSessionName(candidate.thread), ".json") + ".lock")
			if err != nil || !leased {
				return err
			}
			defer release()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.advanceStorageRevision(); err != nil {
			return err
		}
		// Lexical order puts call envelopes before snapshot objects. Index
		// retirement is published in this same protected commit.
		for _, name := range names {
			if !deleted[name] {
				continue
			}
			if err := os.Remove(filepath.Join(s.directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			freed += plan.snapshot.files[name]
			delete(plan.snapshot.files, name)
		}
		for _, name := range slices.Sorted(maps.Keys(encoded)) {
			if err := s.writeFile(name, "changes-pending-", encoded[name]); err != nil {
				return err
			}
			freed += plan.snapshot.files[name] - int64(len(encoded[name]))
			plan.snapshot.files[name] = int64(len(encoded[name]))
			plan.indexes[name] = indexes[name]
		}
		// Keep the original ownership catalog until its last batch completes.
		// A restart can safely plan again from that catalog, including no-op
		// removals of already-retired names. This avoids quadratic catalog
		// rewrites while preserving surviving dependencies and other owners.
		if candidate.name != "" && len(candidate.files) == len(names) {
			if err := os.Remove(filepath.Join(s.directory, candidate.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			freed += plan.snapshot.files[candidate.name]
			delete(plan.snapshot.files, candidate.name)
		}
		for _, name := range names {
			plan.snapshot.owners[name]--
			delete(candidate.files, name)
		}
		candidate.pending = candidate.pending[len(names):]
		plan.revision, err = s.storageRevision()
		committed = err == nil
		return err
	})
	return committed, freed, err
}

func (s *mekugiReplayStore) tryPruneLease(name string) (bool, func(), error) {
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

// This method is called by the router-owned worker (or explicit offline tests),
// never by request preparation, UI/session startup or failure publication.
func (s *mekugiReplayStore) cleanupSessions(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	leased, release, err := s.tryPruneLease("retention-maintenance.lock")
	if err != nil || !leased {
		return err
	}
	defer release()
	plan, err := s.planStoragePrune(ctx)
	if errors.Is(err, errStoragePruneStale) {
		return nil
	}
	if err != nil {
		return err
	}
	if plan == nil {
		return nil
	}
	cutoff := time.Now().Add(-sessionRetention)
	removed, legacy, freed := 0, 0, int64(0)
	announced := false
	defer func() {
		if freed > 0 && s.storageNotice != nil {
			s.storageNotice("", "", "reclaimed", fmt.Sprintf("Mekugi background storage cleanup reclaimed %d bytes from %d inactive sessions and %d legacy records. Codex chats and workspace files are unchanged.", freed, removed, legacy))
		}
	}()
	for i := range plan.snapshot.sessions {
		candidate := &plan.snapshot.sessions[i]
		old := plan.expire && candidate.used.Before(cutoff)
		if !old && !s.pruneRequired(plan) || candidate.thread != "" && candidate.thread == s.session.Thread || candidate.legacy && candidate.thread == "" && !candidate.used.Before(cutoff) {
			continue
		}
		if s.storageNotice != nil && !announced {
			s.storageNotice("", "", "planning", fmt.Sprintf("Mekugi background storage cleanup inspected %d managed records; reclaiming eligible inactive data in batches of at most %d files.", len(plan.snapshot.files), storagePruneBatchFiles))
			announced = true
		}
		completed := false
		for {
			committed, bytes, err := s.commitStoragePrune(ctx, plan, candidate)
			if errors.Is(err, errStoragePruneStale) || errors.Is(err, errStoragePruneBusy) {
				return nil // Retry after concurrent work, never use stale owners.
			}
			if err != nil {
				return fmt.Errorf("background session cleanup partially completed: %w", err)
			}
			if !committed {
				break // Active work or a reader's snapshot lease protects it.
			}
			freed += bytes
			if len(candidate.files) == 0 {
				completed = true
				break
			}
		}
		if completed {
			if candidate.legacy {
				legacy++
			} else {
				removed++
			}
		}
	}
	return s.locked(ctx, func() error {
		revision, err := s.storageRevision()
		if err != nil || revision != plan.revision {
			return err
		}
		if plan.pressure != nil {
			if err := s.advanceStorageRevision(); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(s.directory, "retention-pressure")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := s.writeFile("retention-sweep", "retention-pending-", nil); err != nil {
			return err
		}
		// The empty marker is intentionally content-stable; refresh its clock even
		// when publication skipped an identical rewrite.
		now := time.Now()
		return os.Chtimes(filepath.Join(s.directory, "retention-sweep"), now, now)
	})
}
