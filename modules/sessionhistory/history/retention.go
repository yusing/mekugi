package history

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
)

const SessionRetention = 14 * 24 * time.Hour

type maintenance struct {
	*Store
	writes       *persistence.Counter
	limits       Limits
	activeThread string
	notice       func(phase, message string)
}

// Cleanup performs serialized, bounded retirement of an outside-lock plan.
// Concurrent publication or active readers postpone cleanup without blocking.
func (s *Store) Cleanup(ctx context.Context, writes *persistence.Counter, limits Limits, activeThread string, notice func(phase, message string)) error {
	m := &maintenance{Store: s, writes: writes, limits: limits, activeThread: activeThread, notice: notice}
	return m.cleanupSessions(ctx)
}

// PendingCleanup observes the coalesced pressure marker while serialized.
func (tx *Transaction) PendingCleanup() (bool, error) {
	if err := tx.check(); err != nil {
		return false, err
	}
	request, err := tx.store.readStoragePressure()
	return request != nil, err
}

func (s *Store) retirementPlanners(ctx context.Context, names []string) (map[string]recordkind.RetirementPlanner, error) {
	planners := make(map[string]recordkind.RetirementPlanner)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind, _, err := s.service.Kind(name)
		if err != nil {
			return nil, err
		}
		if kind.Retirement == nil {
			continue
		}
		data, err := ReadRecord(filepath.Join(s.directory, name))
		if err != nil {
			return nil, err
		}
		planner, err := kind.Retirement(ctx, recordkind.Record{Name: name, Data: data})
		if err != nil {
			return nil, err
		}
		if planner != nil {
			planners[name] = planner
		}
	}
	return planners, nil
}

// Source: internal/router/storage_prune.go@b8ca48c7 bounded background retirement.
const storagePruneBatchFiles = 16

var errStoragePruneStale = errors.New("storage changed during background prune planning")
var errStoragePruneBusy = errors.New("inherited snapshot validation is active")

type storagePressureRequest struct {
	Name   string `json:"name"`
	Growth int64  `json:"growth"`
}

func (tx *Transaction) requestStoragePrune(request storagePressureRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	// One coalesced store-wide request contains only a managed name and byte
	// growth, never request views, tool inputs, thread ancestry or process handles.
	return tx.Write("retention-pressure", "pressure-pending-", data)
}

func (s *Store) readStoragePressure() (*storagePressureRequest, error) {
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
	if (request.Name != "" && !s.registered(request.Name) && !CatalogName(request.Name)) || request.Growth < 0 || request.Growth > 2*MaxRecordBytes {
		return nil, errors.New("invalid storage pressure request")
	}
	return &request, nil
}

type storagePrunePlan struct {
	revision   string
	snapshot   storageSnapshot
	retirement map[string]recordkind.RetirementPlanner
	pressure   *storagePressureRequest
	expire     bool
	usage      storageUsage
}

func (s *maintenance) planStoragePrune(ctx context.Context) (*storagePrunePlan, error) {
	plan := &storagePrunePlan{}
	err := s.Store.Transaction(ctx, s.writes, func(tx *Transaction) error {
		var err error
		plan.revision, err = tx.Revision()
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
	plan.snapshot, err = s.storageSnapshotContext(ctx, s.limits, plan.pressure, plan.expire)
	if err == nil && plan.snapshot.unknown == "" && len(plan.snapshot.sessions) > 0 {
		plan.retirement, err = s.retirementPlanners(ctx, slices.Sorted(maps.Keys(plan.snapshot.files)))
		name := ""
		if plan.pressure != nil {
			name = plan.pressure.Name
		}
		plan.usage = s.storageUsage(plan.snapshot.files, name)
	}
	readErr := err
	err = s.Store.Transaction(ctx, s.writes, func(tx *Transaction) error {
		revision, err := tx.Revision()
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

func (s *maintenance) pruneRequired(plan *storagePrunePlan) bool {
	usage, size := plan.usage, int64(0)
	if plan.pressure != nil {
		size = plan.snapshot.files[plan.pressure.Name] + plan.pressure.Growth
		usage.total += plan.pressure.Growth
		usage.own += plan.pressure.Growth
	}
	required, limit := usage.required(s.limits, size)
	return required > limit
}

func (s *maintenance) resizePruneRecord(plan *storagePrunePlan, name string, size int64) {
	kind, _, _ := s.service.Kind(name)
	plan.usage.add(kind, size-plan.snapshot.files[name])
	plan.snapshot.files[name] = size
}

// One commit has a bounded file batch. All decoding, composition and encoding
// happen before acquiring store.lock; leases and revision are checked again
// immediately before effects. No lock is held between batches.
func (s *maintenance) commitStoragePrune(ctx context.Context, plan *storagePrunePlan, candidate *storageCandidate) (bool, int64, error) {
	if candidate.pending == nil {
		var err error
		candidate.pending, err = dependencyOrder(candidate.files, plan.snapshot.dependencies)
		if err != nil {
			return false, 0, err
		}
	}
	names := candidate.pending
	names = names[:min(len(names), storagePruneBatchFiles)]
	deleted := make(map[string]bool)
	for _, name := range names {
		kind, found, err := s.service.Kind(name)
		if err != nil {
			return false, 0, err
		}
		if plan.snapshot.owners[name] <= 1 && found && !kind.Keep {
			deleted[name] = true
		}
	}
	updates := make(map[string]*recordkind.RetirementUpdate)
	for name, prepare := range plan.retirement {
		update, err := prepare(ctx, recordkind.Retirement{Deleted: deleted, Thread: candidate.thread})
		if err != nil {
			return false, 0, err
		}
		if update != nil {
			updates[name] = update
		}
	}
	committed, freed := false, int64(0)
	err := s.Store.Transaction(ctx, s.writes, func(tx *Transaction) error {
		revision, err := tx.Revision()
		if err != nil {
			return err
		}
		if revision != plan.revision {
			return errStoragePruneStale
		}
		snapshotLease, release, err := s.TryPruneLease("retention-snapshot.lock")
		if err != nil {
			return err
		}
		if !snapshotLease {
			return errStoragePruneBusy
		}
		defer release()
		if candidate.thread != "" {
			leased, release, err := s.TryPruneLease(strings.TrimSuffix(SessionName(candidate.thread), ".json") + ".lock")
			if err != nil || !leased {
				return err
			}
			defer release()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tx.AdvanceRevision(); err != nil {
			return err
		}
		// Dependency order retires referrers before their payloads. Owner
		// retirement is published in this same protected commit.
		for _, name := range names {
			if !deleted[name] {
				continue
			}
			if err := os.Remove(filepath.Join(s.directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			freed += plan.snapshot.files[name]
			s.resizePruneRecord(plan, name, 0)
			delete(plan.snapshot.files, name)
		}
		for _, name := range slices.Sorted(maps.Keys(updates)) {
			if err := tx.Write(name, "retirement-pending-", updates[name].Data); err != nil {
				return err
			}
			freed += plan.snapshot.files[name] - int64(len(updates[name].Data))
			s.resizePruneRecord(plan, name, int64(len(updates[name].Data)))
			plan.retirement[name] = updates[name].Next
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
			s.resizePruneRecord(plan, candidate.name, 0)
			delete(plan.snapshot.files, candidate.name)
		}
		for _, name := range names {
			plan.snapshot.owners[name]--
			delete(candidate.files, name)
		}
		candidate.pending = candidate.pending[len(names):]
		plan.revision, err = tx.Revision()
		committed = err == nil
		return err
	})
	return committed, freed, err
}

// This method is called by the router-owned worker (or explicit offline tests),
// never by request preparation, UI/session startup or failure publication.
func (s *maintenance) cleanupSessions(ctx context.Context) error {
	if s == nil {
		return nil
	}
	leased, release, err := s.TryPruneLease("retention-maintenance.lock")
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
	if plan.snapshot.unknown != "" {
		return fmt.Errorf("session cleanup blocked by unrecognized retained record %q; restore its owning module or migrate the retained store before retrying cleanup. All retained history was preserved", plan.snapshot.unknown)
	}
	cutoff := time.Now().Add(-SessionRetention)
	removed, legacy, freed := 0, 0, int64(0)
	announced := false
	defer func() {
		if freed > 0 && s.notice != nil {
			s.notice("reclaimed", fmt.Sprintf("Mekugi background storage cleanup reclaimed %d bytes from %d inactive sessions and %d legacy records. Codex chats and workspace files are unchanged.", freed, removed, legacy))
		}
	}()
	for i := range plan.snapshot.sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidate := &plan.snapshot.sessions[i]
		old := plan.expire && candidate.used.Before(cutoff)
		if candidate.thread != "" && candidate.thread == s.activeThread || candidate.legacy && candidate.thread == "" && !candidate.used.Before(cutoff) || !old && !s.pruneRequired(plan) {
			continue
		}
		if s.notice != nil && !announced {
			s.notice("planning", fmt.Sprintf("Mekugi background storage cleanup inspected %d managed records; reclaiming eligible inactive data in batches of at most %d files.", len(plan.snapshot.files), storagePruneBatchFiles))
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
	return s.Store.Transaction(ctx, s.writes, func(tx *Transaction) error {
		revision, err := tx.Revision()
		if err != nil || revision != plan.revision {
			return err
		}
		if plan.pressure != nil {
			if err := tx.AdvanceRevision(); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(s.directory, "retention-pressure")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := tx.Write("retention-sweep", "retention-pending-", nil); err != nil {
			return err
		}
		// The empty marker is intentionally content-stable; refresh its clock even
		// when publication skipped an identical rewrite.
		now := time.Now()
		return os.Chtimes(filepath.Join(s.directory, "retention-sweep"), now, now)
	})
}
