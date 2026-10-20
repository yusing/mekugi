package launch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"maps"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/recordtransactions"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
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
	History      legacycall.History
	Snapshots    *legacycall.Snapshots `json:",omitempty"`
}

func (s *mekugiReplayStore) read(workspace, callID string, commentary bool) (replayRecord, bool, error) {
	record, found, err := s.publication.ReadHostCall(workspace, callID, commentary)
	if err != nil || !found {
		return replayRecord{}, found, err
	}
	return replayRecord{Version: record.Version, Workspace: record.Workspace, CallID: record.CallID, Commentary: record.Commentary, CaptureOrder: record.CaptureOrder, History: record.History}, true, nil
}
func (s *mekugiReplayStore) lookup(ctx context.Context, workspace, callID string) (legacycall.History, bool, error) {
	if s == nil {
		return legacycall.History{}, false, nil
	}
	value, found, err := s.publication.LookupHostCall(ctx, workspace, callID)
	return value, found, err
}
func (s *mekugiReplayStore) write(r replayRecord) error {
	return requestexecution.StoragePublicationError(s.transaction.PublishHostCall(s.session.Thread, s.retentionLimits(), legacycall.Record{Version: r.Version, Workspace: r.Workspace, CallID: r.CallID, Commentary: r.Commentary, CaptureOrder: r.CaptureOrder, History: r.History, Snapshots: r.Snapshots}, r.History.ChangeID != "" && len(r.History.ReviewFiles) > 0))
}

// These identities select fixture records only. Requests select their own scope
// through executionobservation.Preparer.
type storageSessionKey struct{}
type storageSessionIdentity struct{ Thread, Routing, Namespace string }

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

func (s *mekugiReplayStore) retentionScope(ctx context.Context) retentionScope {
	s = s.scoped(ctx)
	return retentionScope{store: s.publication, writes: s.writes, limits: s.retentionLimits(), thread: s.session.Thread, notice: func(phase, message string) {
		if s.storageNotice != nil {
			s.storageNotice("", "", phase, message)
		}
	}}
}
func (s *mekugiReplayStore) changeScope() changes.Scope {
	return changes.Scope{Store: s.publication, Namespace: s.session.Namespace, Thread: s.session.Thread, Limits: s.retentionLimits(), Writes: s.writes}
}

// put seeds neutral retained evidence through the actual publication owners.
func (s *mekugiReplayStore) put(ctx context.Context, workspace string, histories map[string]legacycall.History) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	publications := make(map[string]changes.Publication, len(histories))
	for id, h := range histories {
		managed := len(h.ReviewFiles) > 0
		for _, f := range h.ReviewFiles {
			managed = managed && f.Origin != ""
		}
		publications[id] = changes.Publication{ChangeID: h.ChangeID, CorrelationID: h.CorrelationID, ExecutingThread: h.ExecutingThread, Managed: managed}
	}
	updates, err := recordtransactions.PublishEvidence(ctx, s.changeScope(), s.changes, workspace, publications, func(tx *history.Transaction) error {
		for _, id := range slices.Sorted(maps.Keys(histories)) {
			h := histories[id]
			if err := tx.PublishHostCall(s.session.Thread, s.retentionLimits(), legacycall.Record{Version: 1, Workspace: workspace, CallID: id, History: h}, h.ChangeID != "" && len(h.ReviewFiles) > 0); err != nil {
				return requestexecution.StoragePublicationError(err)
			}
		}
		return nil
	})
	if err == nil && s.liveDiff != nil {
		s.liveDiff(updates)
	}
	return err
}
func (s *mekugiReplayStore) retainInput(ctx context.Context, workspace string, raw json.RawMessage, histories map[string]legacycall.History, release func()) error {
	if len(raw) != 0 {
		return errors.New("integration fixture requires explicit retained references")
	}
	s = s.scoped(ctx)
	input := recordtransactions.Input{Workspace: workspace, ReleaseSnapshot: release}
	for id, h := range histories {
		input.Files = append(input.Files, legacycall.Name(workspace, id, false))
		input.Changes = append(input.Changes, h.ChangeID)
	}
	return recordtransactions.RetainInput(ctx, s.changeScope(), s.changes, s.recovery, input)
}

// Fixture composition supplies production owners before preparing an operation.
func setTestProxyStore(p *observationFixture, store *mekugiReplayStore) {
	p.retained, p.changeSource, p.snapshots, p.recovered, p.publishChanges = store.changeScope(), store.changes, store.snapshots, store.recovery, store.liveDiff
}
func testProxyWithStore(store *mekugiReplayStore, windows ...*executionobservation.Windows) *observationFixture {
	p := new(observationFixture)
	setTestProxyStore(p, store)
	if len(windows) != 0 {
		p.execWindows = windows[0]
	}
	return p
}
func setTestUIStore(u *appServerUI, store *mekugiReplayStore) {
	u.retained, u.changeSource, u.snapshots, u.recovered, u.publishChanges = store.changeScope(), store.changes, store.snapshots, store.recovery, store.liveDiff
}
func observationTestStore(selected changes.Scope, source changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publish func([]changes.Update)) *mekugiReplayStore {
	if selected.Store == nil {
		return nil
	}
	return &mekugiReplayStore{publication: selected.Store, directory: selected.Store.Directory(), writes: selected.Writes, maxBytes: selected.Limits.Total, maxCommentaryBytes: selected.Limits.Kinds["commentary"], session: storageSessionIdentity{Thread: selected.Thread, Namespace: selected.Namespace}, changes: source, snapshots: snapshots, recovery: recovered, liveDiff: publish}
}
func testProxyStore(p *observationFixture) *mekugiReplayStore {
	return observationTestStore(p.retained, p.changeSource, p.snapshots, p.recovered, p.publishChanges)
}

func testUIStore(u *appServerUI) *mekugiReplayStore {
	return observationTestStore(u.retained, u.changeSource, u.snapshots, u.recovered, u.publishChanges)
}

const maxReplayRecordBytes = history.MaxRecordBytes
const defaultReplayStorageBytes = history.DefaultStorageBytes
const sessionRetention = history.SessionRetention
