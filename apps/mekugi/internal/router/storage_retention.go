package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/recordtransactions"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
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
	for _, session := range p.calls.PinnedSessions() {
		if strings.HasSuffix(session, "\x00"+thread) {
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
	if conflict, ok := errors.AsType[*history.HostConflict](err); ok {
		if conflict.Field == "" {
			return requestexecution.Classify(err, "replay_translation_conflict", "a completed tool call conflicted with its retained replay translation", true)
		}
		return requestexecution.Classify(err, "replay_item_conflict", "a completed tool call changed a retained replay item field", true)
	}
	if capacity, ok := errors.AsType[*history.CapacityError](err); ok {
		return requestexecution.Classify(err, "storage_capacity", capacity.Error(), false)
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
	if s.transaction != nil {
		return errors.New("nested replay publication transaction")
	}
	input := recordtransactions.Input{Workspace: workspace, ReleaseSnapshot: releaseSnapshot}
	for call, item := range histories {
		input.Files = append(input.Files, legacycall.Name(workspace, call, false))
		input.Changes = append(input.Changes, item.ChangeID)
	}
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &items)
	for _, item := range items {
		if responses.StringField(item, "type") == "message" {
			input.OptionalFiles = append(input.OptionalFiles, legacycall.Name(workspace, responses.StringField(item, "id"), true))
		}
	}
	for _, match := range retainedReadReference.FindAllStringSubmatch(string(raw), -1) {
		input.References = append(input.References, strings.Fields(strings.ReplaceAll(match[1], `\t`, " "))...)
	}
	if identity := s.session; identity.Initialize {
		input.Initialize = &history.Scope{Thread: identity.Thread, Namespace: identity.Namespace, Parent: identity.Parent, Fork: identity.Fork}
	}
	err := recordtransactions.RetainInput(ctx, s.changeScope(), s.changes, s.recovery, input)
	if retention, ok := errors.AsType[*recordtransactions.RetentionError](err); ok {
		return storagePublicationError(retention.Err)
	}
	return changeError(err)
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
	return requestexecution.Classify(err, "storage_io", "Mekugi could not persist session data; check available disk space and storage permissions before retrying", false)
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
type retentionScope struct {
	store  *history.Store
	writes *persistence.Counter
	limits history.Limits
	thread string
	notice func(string, string)
}

func (s *mekugiReplayStore) retentionScope(ctx context.Context) retentionScope {
	s = s.scoped(ctx)
	return retentionScope{store: s.publication, writes: s.writes, limits: s.retentionLimits(), thread: s.session.Thread, notice: func(phase, message string) {
		if s.storageNotice != nil {
			s.storageNotice("", "", phase, message)
		}
	}}
}
func runStorageRetention(ctx context.Context, diagnostics *diagnostic.Service, store func() retentionScope, notice func(error)) {
	var debugSweep, sessionSweep time.Time
	cleanup := func() {
		if diagnostics != nil && time.Since(debugSweep) >= time.Hour {
			debugSweep = time.Now()
			root, err := debugStorageDirectory()
			if err == nil {
				err = diagnostics.CleanupBundles(ctx, root, debugSweep)
			}
			if err != nil && ctx.Err() == nil && notice != nil {
				notice(fmt.Errorf("debug bundle cleanup: %w", err))
			}
		}
		s := store()
		if s.store == nil {
			return
		}
		var err error
		if time.Since(sessionSweep) < time.Minute {
			// Bound full age replanning when publications invalidate ownership.
			// Pressure still gets the worker's one-second response interval.
			var pressure bool
			err = s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
				var err error
				pressure, err = tx.PendingCleanup()
				return err
			})
			if err == nil && !pressure {
				return
			}
		}
		if err == nil {
			sessionSweep = time.Now()
			err = s.store.Cleanup(ctx, s.writes, s.limits, s.thread, s.notice)
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
