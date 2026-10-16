package router

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const maxReplayRecordBytes = history.MaxRecordBytes

// Durable records are immutable translation facts. Request-local confirmation and
// ordering are intentionally absent. Session retention removes only inactive, unshared records.
type mekugiReplayStore struct {
	changes            changes.Service
	recovery           *recovery.Service
	writes             *persistence.Counter
	directory          string
	maxBytes           int64
	session            storageSessionIdentity
	storageNotice      func(session, thread, phase, message string)
	liveDiff           func([]reviewChange)
	maxCommentaryBytes int64
	snapshots          *workspaceSnapshots
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

// Keep request-local state out of immutable replay comparisons as well as JSON.
func durableHistory(h mekugiHistory) mekugiHistory {
	h.bytes, h.confirmed, h.sequence = 0, false, 0
	h.nativeCell = nil
	if len(h.NativePatches) == 0 {
		h.NativePatches = nil // Empty omitted slices read back as nil.
	}
	if h.ResolvedBaseline != nil {
		baseline := *h.ResolvedBaseline
		baseline.Files = slices.Clone(baseline.Files)
		for i := range baseline.Files {
			baseline.Files[i].watchStamp = ""
		}
		baseline.Inventory = baseline.Inventory.durable()
		h.ResolvedBaseline = &baseline
	}
	if h.ExecObservation != nil {
		observation := *h.ExecObservation
		observation.WindowStart = observation.WindowStart.UTC()
		observation.Files = slices.Clone(observation.Files)
		for i := range observation.Files {
			// Live-preview state is not serialized. Keep the request's copy
			// while comparing against the persisted capture.
			observation.Files[i].watchStamp = ""
			observation.Files[i].watchMoveOnly = false
		}
		observation.Listings = slices.Clone(observation.Listings)
		for i := range observation.Listings {
			if len(observation.Listings[i].Entries) == 0 {
				observation.Listings[i].Entries = nil // Omitted empty maps read back as nil.
			}
		}
		observation.Inventory = observation.Inventory.durable()
		h.ExecObservation = &observation
	}
	return h
}

func defaultMekugiReplayDirectory() (string, error) {
	base, err := history.StateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "replay"), nil
}
func openReplayStore(ctx context.Context, service *history.Service, directory string) (*mekugiReplayStore, error) {
	publication, err := service.Open(ctx, directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	return &mekugiReplayStore{directory: directory, maxBytes: defaultReplayStorageBytes, maxCommentaryBytes: 16 << 20, snapshots: newWorkspaceSnapshots(directory), publication: publication}, nil
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
	record, found, err := legacycall.Read(s.directory, workspace, callID, commentary, maxReplayRecordBytes)
	if err != nil || !found {
		return replayRecord{}, found, err
	}
	return replayRecord{Version: record.Version, Workspace: record.Workspace, CallID: record.CallID, Commentary: record.Commentary, CaptureOrder: record.CaptureOrder, History: legacyHistory(record.History)}, true, nil
}
func (s *mekugiReplayStore) lookup(ctx context.Context, workspace, callID string) (h mekugiHistory, found bool, err error) {
	if s == nil {
		return h, false, nil
	}
	err = s.locked(ctx, func(s *mekugiReplayStore) error {
		r, ok, e := s.read(workspace, callID, false)
		h = r.History
		found = ok
		return e
	})
	return
}
func (s *mekugiReplayStore) hasCommentary(ctx context.Context, workspace, id string) (found bool, err error) {
	if s == nil {
		return false, nil
	}
	err = s.locked(ctx, func(s *mekugiReplayStore) error {
		_, ok, e := s.read(workspace, id, true)
		found = ok
		return e
	})
	return
}
func (s *mekugiReplayStore) put(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	return s.locked(ctx, func(s *mekugiReplayStore) error {
		for _, id := range slices.SortedFunc(maps.Keys(histories), func(a, b string) int {
			return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
		}) {
			if err := s.write(replayRecord{Version: 1, Workspace: workspace, CallID: id, History: durableHistory(histories[id])}); err != nil {
				return err
			}
		}
		updates, err := s.changes.Publish(s.transaction, s.changeScope(), workspace, changePublications(histories))
		if err == nil {
			s.notifyChanges(updates)
		}
		return changeError(err)
	})
}

// Upstream completion may add fields or finalize status after an SSE item is
// already durable, including opaque provider passthrough metadata. It cannot
// alter the original model payload or translation.
func mergeReplayHistory(old, next mekugiHistory) (mekugiHistory, error) {
	// Older retained timestamps can carry an offset even when the current
	// file-clock capture is UTC. Compare their durable instants, not locations.
	old, next = durableHistory(old), durableHistory(next)
	oldItem, nextItem := old.UpstreamItem, next.UpstreamItem
	oldIDs, nextIDs := old.CommentaryMessageIDs, next.CommentaryMessageIDs
	old.UpstreamItem = nil
	next.UpstreamItem = nil
	old.CommentaryMessageIDs = nil
	next.CommentaryMessageIDs = nil
	if !reflect.DeepEqual(old, next) {
		return next, criticalDiagnostic(errors.New("conflicting durable replay translation"),
			"replay_translation_conflict", "a completed tool call conflicted with its retained replay translation", true)
	}
	merged := make(map[string]json.RawMessage, len(oldItem)+len(nextItem))
	maps.Copy(merged, oldItem)
	for k, v := range nextItem {
		previous, exists := merged[k]
		// Persistence compacts RawMessage whitespace. Compare that spelling,
		// preserving string escapes and object order rather than decoding values.
		var compact bytes.Buffer
		if err := json.Compact(&compact, v); err != nil {
			return next, fmt.Errorf("invalid durable replay item field %q: %w", k, err)
		}
		v = bytes.Clone(compact.Bytes())
		if exists {
			compact.Reset()
			if err := json.Compact(&compact, previous); err != nil {
				return next, fmt.Errorf("invalid retained replay item field %q: %w", k, err)
			}
			previous = compact.Bytes()
			// The provider enriches this opaque metadata between input.done and
			// output_item.done. Preserve its latest spelling for replay without
			// relaxing checks on tool identity, input, or other item fields.
			metadata := k == "internal_chat_message_metadata_passthrough"
			finalized := k == "status" && string(previous) == `"in_progress"` &&
				(string(v) == `"completed"` || string(v) == `"incomplete"`)
			if !bytes.Equal(previous, v) && !metadata && !finalized {
				return next, criticalDiagnostic(fmt.Errorf("conflicting durable replay item field %q", k),
					"replay_item_conflict", "a completed tool call changed a retained replay item field", true)
			}
		}
		merged[k] = v
	}
	if len(merged) > 0 {
		next.UpstreamItem = merged
	}
	next.CommentaryMessageIDs = slices.Clone(oldIDs)
	for _, id := range nextIDs {
		if !slices.Contains(next.CommentaryMessageIDs, id) {
			next.CommentaryMessageIDs = append(next.CommentaryMessageIDs, id)
		}
	}
	return next, nil
}
func (s *mekugiReplayStore) write(r replayRecord) (err error) {
	previous, exists, err := s.read(r.Workspace, r.CallID, r.Commentary)
	if err != nil {
		return err
	}
	if exists {
		r.CaptureOrder = previous.CaptureOrder
		r.History, err = mergeReplayHistory(previous.History, r.History)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(previous, r) {
			return nil
		}
	}
	failed := r.History.ExecOutcome != nil && r.History.ExecOutcome.Status == execStatusFailed && r.History.ExecOutcome.SharedWith == ""
	if !exists && !r.Commentary && (r.History.ChangeID != "" && len(r.History.ReviewFiles) > 0 || failed) {
		r.CaptureOrder, err = s.transaction.ReserveCaptureOrder()
		if err != nil {
			return err
		}
	}
	data, err := responses.MarshalJSON(r)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return storageCapacityError("replay record", int64(len(data)), maxReplayRecordBytes, "Split the tool call or reduce its retained output before retrying.")
	}
	name := legacycall.Name(r.Workspace, r.CallID, r.Commentary)
	prefix := "call-"
	if r.Commentary {
		prefix = "commentary-"
	}
	return s.writeManagedFile(name, prefix+"pending-", data)
}
