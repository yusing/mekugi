package router

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/recordtransactions"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/persistence"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const maxReplayRecordBytes = history.MaxRecordBytes

// Durable records are immutable translation facts. Request-local confirmation and
// ordering are intentionally absent. Session retention removes only inactive, unshared records.
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
type replayRecord struct {
	Version      int
	Workspace    string
	CallID       string
	Commentary   bool
	CaptureOrder uint64 `json:",omitzero"`
	History      mekugiHistory
	Snapshots    *legacycall.Snapshots `json:",omitempty"`
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
func (s *mekugiReplayStore) put(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	if s.transaction != nil {
		return errors.New("nested replay publication transaction")
	}
	updates, err := recordtransactions.PublishEvidence(ctx, s.changeScope(), s.changes, workspace, changePublications(histories), func(tx *history.Transaction) error {
		writer := *s
		writer.transaction = tx
		for _, id := range slices.SortedFunc(maps.Keys(histories), func(a, b string) int {
			return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
		}) {
			if err := writer.write(replayRecord{Version: 1, Workspace: workspace, CallID: id, History: histories[id]}); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.notifyChanges(updates)
	}
	return changeError(err)
}

// The application classifies capture eligibility; Session History owns merging,
// ordering and publication of the neutral retained envelope.
func (s *mekugiReplayStore) write(r replayRecord) error {
	failed := r.History.ExecOutcome != nil && r.History.ExecOutcome.Status == changecapture.StatusFailed && r.History.ExecOutcome.SharedWith == ""
	ordered := r.History.ChangeID != "" && len(r.History.ReviewFiles) > 0 || failed
	record := legacycall.Record{Version: r.Version, Workspace: r.Workspace, CallID: r.CallID, Commentary: r.Commentary, CaptureOrder: r.CaptureOrder, History: retainedHistory(r.History), Snapshots: r.Snapshots}
	err := s.transaction.PublishHostCall(s.session.Thread, s.retentionLimits(), record, ordered)
	var publication *history.PublicationError
	var conflict *history.HostConflict
	var capacity *history.CapacityError
	if errors.As(err, &publication) || errors.As(err, &conflict) || errors.As(err, &capacity) {
		return storagePublicationError(err)
	}
	return err
}
