package executionobservation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/persistence"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type replayRecord struct {
	Version      int
	Workspace    string
	CallID       string
	Commentary   bool
	CaptureOrder uint64 `json:",omitzero"`
	History      mekugiHistory
	Snapshots    *legacycall.Snapshots `json:",omitempty"`
}

func (s *mekugiReplayStore) read(workspace, callID string, commentary bool) (replayRecord, bool, error) {
	record, found, err := s.publication.ReadHostCall(workspace, callID, commentary)
	if err != nil || !found {
		return replayRecord{}, found, err
	}
	return replayRecord{Version: record.Version, Workspace: record.Workspace, CallID: record.CallID, Commentary: record.Commentary, CaptureOrder: record.CaptureOrder, History: legacyHistory(record.History)}, true, nil
}
func (s *mekugiReplayStore) lookup(ctx context.Context, workspace, callID string) (mekugiHistory, bool, error) {
	if s == nil {
		return mekugiHistory{}, false, nil
	}
	value, found, err := s.publication.LookupHostCall(ctx, workspace, callID)
	return legacyHistory(value), found, err
}
func (s *mekugiReplayStore) hasCommentary(ctx context.Context, workspace, id string) (bool, error) {
	if s == nil {
		return false, nil
	}
	return s.publication.HasCommentary(ctx, workspace, id)
}
func (s *mekugiReplayStore) write(r replayRecord) error {
	return publishObservation(s.transaction, s.changeScope(), legacycall.Record{Version: r.Version, Workspace: r.Workspace, CallID: r.CallID, Commentary: r.Commentary, CaptureOrder: r.CaptureOrder, History: retainedHistory(r.History), Snapshots: r.Snapshots})
}
func (s *mekugiReplayStore) prepareHandleScope(ctx context.Context, metadata codexresponses.TurnMetadata) (context.Context, error) {
	if s == nil {
		return ctx, nil
	}
	return prepareHandleScope(ctx, s.publication, s.writes, metadata)
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

func (s *mekugiReplayStore) recoverySession(ctx context.Context) (*recovery.Session, error) {
	s = s.scoped(ctx)
	return s.recovery.AttachStore(s.publication, s.session.Namespace, s.session.Thread, s.retentionLimits(), s.writes)
}

// Retained-evidence fixture configuration. Production borrows the actual owners.
type mekugiReplayStore struct {
	changes            changes.Service
	captured           *changecapture.Service
	recovery           *recovery.Service
	writes             *persistence.Counter
	directory          string
	maxBytes           int64
	session            storageSessionIdentity
	storageNotice      func(session, thread, phase, message string)
	liveDiff           func([]changes.Update)
	maxCommentaryBytes int64
	snapshots          *changecapture.Workspaces
	publication        *history.Store
	transaction        *history.Transaction
}

func openReplayStore(ctx context.Context, service *history.Service, captured *changecapture.Service, directory string) (*mekugiReplayStore, error) {
	publication, err := service.Open(ctx, directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	snapshots, err := captured.Checkpoints(directory)
	if err != nil {
		return nil, err
	}
	store := &mekugiReplayStore{directory: directory, maxBytes: defaultReplayStorageBytes, maxCommentaryBytes: 16 << 20, snapshots: snapshots, publication: publication}
	if captured != nil {
		store.captured = captured
		store.changes = captured.Service
	}
	return store, nil
}
func (s *mekugiReplayStore) locked(ctx context.Context, fn func(*mekugiReplayStore) error) error {
	if s.transaction != nil {
		return errors.New("nested replay publication transaction")
	}
	core := s.publication
	return core.Transaction(ctx, s.writes, func(transaction *history.Transaction) error {
		scoped := *s
		scoped.transaction = transaction
		return fn(&scoped)
	})
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

// Called under store.lock. Ownership is recorded before publishing a capability.
// Reading inherited history adds another owner instead of transferring ownership.
func (s *mekugiReplayStore) retentionLimits() history.Limits {
	return history.Limits{Total: s.maxBytes, Kinds: map[string]int64{"commentary": s.maxCommentaryBytes}}
}

func (s *mekugiReplayStore) changeScope() changes.Scope {
	return changes.Scope{Store: s.publication, Namespace: s.session.Namespace, Thread: s.session.Thread, Limits: s.retentionLimits(), Writes: s.writes}
}

func (s *mekugiReplayStore) put(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	if s == nil {
		return nil
	}
	if s.transaction != nil {
		return errors.New("nested replay publication transaction")
	}
	return publishObservations(ctx, s.changeScope(), s.changes, s.liveDiff, workspace, histories)
}
func (s *mekugiReplayStore) retainInput(ctx context.Context, workspace string, raw json.RawMessage, histories map[string]mekugiHistory, release func()) error {
	if s == nil {
		return nil
	}
	if s.transaction != nil {
		return errors.New("nested replay publication transaction")
	}
	return retainObservationInput(ctx, s.changeScope(), s.changes, s.recovery, workspace, raw, histories, release)
}
func (s *mekugiReplayStore) putAfterMaintenance(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	if s == nil {
		return nil
	}
	return publishAfterMaintenance(ctx, s.changeScope(), s.changes, s.liveDiff, workspace, histories)
}
func (s *mekugiReplayStore) agentEditNotice(ctx context.Context, workspace, call string, value mekugiHistory) (string, error) {
	return agentEditNotice(ctx, s.changeScope(), s.changes, workspace, call, value)
}

// Fixture composition supplies production owners before preparing an operation.
func setTestProxyStore(p *Preparer, store *mekugiReplayStore) {
	p.retained, p.changeSource, p.snapshots, p.recovered, p.publishChanges = store.changeScope(), store.changes, store.snapshots, store.recovery, store.liveDiff
}
func testProxyWithStore(store *mekugiReplayStore, windows ...*Windows) *Preparer {
	p := new(Preparer)
	setTestProxyStore(p, store)
	if len(windows) != 0 {
		p.execWindows = windows[0]
	}
	return p
}

func observationTestStore(selected changes.Scope, source changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publish func([]changes.Update)) *mekugiReplayStore {
	if selected.Store == nil {
		return nil
	}
	return &mekugiReplayStore{publication: selected.Store, directory: selected.Store.Directory(), writes: selected.Writes, maxBytes: selected.Limits.Total, maxCommentaryBytes: selected.Limits.Kinds["commentary"], session: storageSessionIdentity{Thread: selected.Thread, Namespace: selected.Namespace}, changes: source, snapshots: snapshots, recovery: recovered, liveDiff: publish}
}
func testProxyStore(p *Preparer) *mekugiReplayStore {
	return observationTestStore(p.retained, p.changeSource, p.snapshots, p.recovered, p.publishChanges)
}
func testResponseStore(t *mekugiResponseTransform) *mekugiReplayStore {
	return observationTestStore(t.retained, t.changeSource, t.snapshots, t.recovered, t.publishChanges)
}

const maxReplayRecordBytes = history.MaxRecordBytes
const defaultReplayStorageBytes = history.DefaultStorageBytes
