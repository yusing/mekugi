package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/internal/responses"
)

const sessionRetention = history.SessionRetention
const defaultReplayStorageBytes = history.DefaultStorageBytes

type storageTurnKey struct{}

type storageSessionKey struct{}

type storageSessionIdentity struct {
	Thread     string
	Routing    string
	Namespace  string
	Parent     string
	Fork       string
	Initialize bool
}

func (s *mekugiReplayStore) scoped(ctx context.Context) *mekugiReplayStore {
	if s == nil {
		return nil
	}
	copy := *s
	if identity, ok := ctx.Value(storageSessionKey{}).(storageSessionIdentity); ok {
		copy.session = identity
	}
	return &copy
}

// A shared process lease protects running requests and workers across routers.
// Lock files have stable inodes and are never unlinked while another process
// could be acquiring them. Their empty contents are not retained chat data.
func (s *mekugiReplayStore) beginSession(ctx context.Context, thread, routing string) (context.Context, func(), error) {
	if s == nil || thread == "" {
		return ctx, func() {}, nil
	}
	identity := storageSessionIdentity{Thread: thread, Routing: routing, Namespace: thread}
	if err := s.locked(ctx, func(s *mekugiReplayStore) error {
		var err error
		identity.Namespace, err = s.publication.NamespaceForThread(thread)
		return err
	}); err != nil {
		return ctx, nil, err
	}
	ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	s = s.scoped(ctx)
	release, err := s.publication.SessionLease(ctx, thread)
	if err != nil {
		return ctx, nil, fmt.Errorf("acquire session storage lease: %w", err)
	}
	err = s.locked(ctx, func(s *mekugiReplayStore) error { return s.retainFiles() })
	if err != nil {
		release()
		return ctx, nil, err
	}
	return ctx, release, nil
}

// A turn lease covers host handoff gaps and yielded tools. Only a delivered
// terminal with no pending tools releases it; shutdown also releases all leases.
func (p *mekugiProxy) beginStorageSession(ctx context.Context, thread, routing string) (context.Context, error) {
	ctx, release, err := p.replayStore.beginSession(ctx, thread, routing)
	if err != nil {
		return ctx, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		release()
		return ctx, errors.New("router closed before session storage could be leased")
	}
	if p.storageLeases == nil {
		p.storageLeases = make(map[string]func())
	}
	if p.storageLeases[thread] != nil {
		release()
	} else {
		p.storageLeases[thread] = release
	}
	p.storageSequence++
	if p.storageTurns == nil {
		p.storageTurns = make(map[string]uint64)
	}
	p.storageTurns[thread] = p.storageSequence
	return context.WithValue(ctx, storageTurnKey{}, p.storageSequence), nil
}

func (p *mekugiProxy) releaseIdleStorageSession(ctx context.Context, thread string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// An older delivered terminal must not release a newer host handoff.
	generation, ok := ctx.Value(storageTurnKey{}).(uint64)
	if !ok || generation != p.storageTurns[thread] {
		return
	}
	for session, active := range p.activeSessions {
		if active != 0 && strings.HasSuffix(session, "\x00"+thread) {
			return
		}
	}
	if release := p.storageLeases[thread]; release != nil {
		release()
		delete(p.storageLeases, thread)
		delete(p.storageTurns, thread)
	}
}

// Called under store.lock. Ownership is recorded before publishing a capability.
// Reading inherited history adds another owner instead of transferring ownership.
func (s *mekugiReplayStore) retentionLimits() history.Limits {
	return history.Limits{Total: s.maxBytes, Kinds: map[string]int64{"commentary": s.maxCommentaryBytes}}
}

func (s *mekugiReplayStore) retainFiles(names ...string) error {
	return storagePublicationError(s.transaction.Retain(s.session.Thread, s.retentionLimits(), names...))
}

func storagePublicationError(err error) error {
	if capacity, ok := errors.AsType[*history.CapacityError](err); ok {
		return criticalDiagnostic(err, "storage_capacity", capacity.Error(), false)
	}
	return storageIOError(err)
}

var retainedReadReference = regexp.MustCompile(`(?:\b|\\[nr])mread(?:[ \t]|\\t)+([a-z]+[0-9]*\b(?:(?:[ \t]|\\t)+[a-z]+[0-9]*\b)*)`)

// Retain the complete visible history in one locked catalog update. A fork
// shares immutable records with its source; removing either chat keeps facts
// referenced by the other. Invalid input never establishes an ownership claim.
func (s *mekugiReplayStore) retainInput(ctx context.Context, workspace string, raw json.RawMessage, histories map[string]mekugiHistory, releaseSnapshot func()) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	if s.session.Thread == "" {
		return nil
	}
	return s.locked(ctx, func(s *mekugiReplayStore) error {
		var names []string
		for call := range histories {
			names = append(names, legacycall.Name(workspace, call, false))
		}
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(raw, &items)
		for _, item := range items {
			if responses.StringField(item, "type") == "message" {
				name := legacycall.Name(workspace, responses.StringField(item, "id"), true)
				if info, err := os.Lstat(filepath.Join(s.directory, name)); err == nil && info.Mode().IsRegular() {
					names = append(names, name)
				}
			}
		}
		if err := s.initializeHandleScope(names, releaseSnapshot); err != nil {
			return err
		}
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		for _, history := range histories {
			if change, exists := index.Changes[history.ChangeID]; exists {
				names = append(names, changeIndexName(workspace, index.Namespace))
				for _, attempt := range change.Calls {
					names = append(names, legacycall.Name(workspace, attempt.ID, false))
				}
			}
		}
		for _, match := range retainedReadReference.FindAllStringSubmatch(string(raw), -1) {
			session, err := s.recoverySession(ctx)
			if err != nil {
				return err
			}
			for id := range strings.FieldsSeq(strings.ReplaceAll(match[1], `\t`, " ")) {
				dependencies, err := session.Dependencies(id)
				if err != nil {
					return err
				}
				names = append(names, dependencies...)
			}
		}
		if releaseSnapshot != nil {
			// store.lock now bridges validation to ownership publication;
			// cleanup inside retainFiles can safely acquire the snapshot lease.
			releaseSnapshot()
		}
		return s.retainFiles(names...)
	})
}

func (s *mekugiReplayStore) changeDependencyNames(workspace string, ids []string) ([]string, error) {
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return nil, err
	}
	names := []string{changeIndexName(workspace, index.Namespace)}
	for _, id := range ids {
		change, exists := index.Changes[id]
		if !exists {
			return nil, missingChangeError(index, id)
		}
		for _, call := range change.Calls {
			names = append(names, legacycall.Name(workspace, call.ID, false))
		}
	}
	return names, nil
}

func storageCapacityError(resource string, required, limit int64, action string) error {
	message := fmt.Sprintf("Mekugi %s needs %d bytes; limit is %d bytes. %s", resource, required, limit, action)
	return criticalDiagnostic(errors.New(message), "storage_capacity", message, false)
}

// Only pressure queued for background maintenance is eligible for a persistence
// retry. Record-size limits and storage I/O errors cannot be cured by waiting.
// Keep completed response facts in memory while the existing maintenance worker
// frees space. Every attempt revalidates and publishes under store.lock; neither
// that lock nor a host operation is held or repeated while waiting.
func (s *mekugiReplayStore) putAfterMaintenance(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	err := s.put(ctx, workspace, histories)
	pressure, ok := errors.AsType[*history.PressureError](err)
	if !ok {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if waitErr := s.waitForStorageMaintenance(ctx); waitErr != nil {
			return errors.Join(err, waitErr)
		}
		var used int64
		if checkErr := s.locked(ctx, func(s *mekugiReplayStore) error {
			files, err := s.transaction.FileSizes()
			if err == nil {
				used, _ = s.publication.Usage(s.retentionLimits(), files, pressure.Name, files[pressure.Name])
			}
			return err
		}); checkErr != nil {
			return errors.Join(err, checkErr)
		}
		// Measure reclamation before retrying: a batch may publish earlier
		// histories before reaching pressure again. Those successful writes
		// must not be mistaken for maintenance making no progress.
		if used >= pressure.Used {
			return err
		}
		err = s.put(ctx, workspace, histories)
		next, ok := errors.AsType[*history.PressureError](err)
		if !ok {
			return err
		}
		pressure = next
	}
}

func (s *mekugiReplayStore) waitForStorageMaintenance(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pending bool
		err := s.locked(ctx, func(s *mekugiReplayStore) error {
			var err error
			pending, err = s.transaction.PendingCleanup()
			return err
		})
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func storageIOError(err error) error {
	if err == nil {
		return nil
	}
	return criticalDiagnostic(err, "storage_io", "Mekugi could not persist session data; check available disk space and storage permissions before retrying", false)
}

func (s *mekugiReplayStore) maintainStorage(replacement string, size int64) error {
	return storagePublicationError(s.transaction.Admit(s.retentionLimits(), replacement, size))
}

// Called under store.lock. Retain and admit the record before publication.
func (s *mekugiReplayStore) writeManagedFile(name, pattern string, data []byte) error {
	if err := s.retainFiles(name); err != nil {
		return storageIOError(err)
	}
	if err := s.maintainStorage(name, int64(len(data))); err != nil {
		return err
	}
	return storageIOError(s.transaction.Write(name, pattern, data))
}

// Readers acquire this lease before store.lock; cleanup only tries it without
// waiting while holding store.lock. Thus input validation can span several
// reads without allowing another router to retire its not-yet-adopted history.
func (s *mekugiReplayStore) lockStorageSnapshot(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	return s.publication.SnapshotLease(ctx)
}

// Age-based cleanup is router maintenance, not part of any request's replay
// view. An unrelated catalog must not prevent a new thread from starting.
func runStorageRetention(ctx context.Context, store func() *mekugiReplayStore, notice func(error)) {
	var debugSweep, sessionSweep time.Time
	cleanup := func() {
		if time.Since(debugSweep) >= time.Hour {
			debugSweep = time.Now()
			if err := cleanupDebugBundles(ctx, debugSweep); err != nil && ctx.Err() == nil && notice != nil {
				notice(fmt.Errorf("debug bundle cleanup: %w", err))
			}
		}
		s := store()
		if s == nil {
			return
		}
		var err error
		if time.Since(sessionSweep) < time.Minute {
			// Bound full age replanning when publications invalidate ownership.
			// Pressure still gets the worker's one-second response interval.
			var pressure bool
			err = s.locked(ctx, func(s *mekugiReplayStore) error {
				var err error
				pressure, err = s.transaction.PendingCleanup()
				return err
			})
			if err == nil && !pressure {
				return
			}
		}
		if err == nil {
			sessionSweep = time.Now()
			err = s.cleanupSessions(ctx)
		}
		if err != nil && ctx.Err() == nil && notice != nil {
			notice(fmt.Errorf("session cleanup: %w", err))
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
