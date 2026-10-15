package router

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/outputrecovery/recovery"

	"github.com/yusing/mekugi/internal/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const maxReplayRecordBytes = history.MaxRecordBytes

// Durable records are immutable translation facts. Request-local confirmation and
// ordering are intentionally absent. Session retention removes only inactive, unshared records.
type mekugiReplayStore struct {
	recovery           *recovery.Service
	writes             *persistence.Counter
	directory          string
	maxBytes           int64
	session            storageSessionIdentity
	storageNotice      func(session, thread, phase, message string)
	liveDiff           func([]liveDiffChange)
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
	Snapshots    *replaySnapshots `json:",omitempty"`
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

func mekugiStateDirectory() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(base, "mekugi"), nil
}
func defaultMekugiReplayDirectory() (string, error) {
	base, err := mekugiStateDirectory()
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
func replayRecordName(workspace, callID string, commentary bool) string {
	prefix := "call-"
	if commentary {
		prefix = "commentary-"
	}
	return prefix + fmt.Sprintf("%x.json", sha256.Sum256(fmt.Appendf(nil, "%t\x00%s\x00%s", commentary, workspace, callID)))
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
	name := filepath.Join(s.directory, replayRecordName(workspace, callID, commentary))
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return replayRecord{}, false, nil
	}
	if err != nil {
		return replayRecord{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
		return replayRecord{}, false, errors.New("invalid replay record file")
	}
	f, err := os.Open(name)
	if err != nil {
		return replayRecord{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReplayRecordBytes+1))
	if err != nil {
		return replayRecord{}, false, err
	}
	if len(data) > maxReplayRecordBytes {
		return replayRecord{}, false, errors.New("replay record exceeds size limit")
	}
	var r replayRecord
	// Older records contain a tool-success flag in History. Accept it only
	// at the wire boundary; it is not part of the saved filesystem changes.
	wire := struct {
		*replayRecord
		History struct {
			*mekugiHistory
			LegacyToolSuccess bool `json:"Applied"`
			ExecObservation   *struct {
				execObservation
				LegacySweep bool `json:"Sweep"`
			}
		}
	}{replayRecord: &r}
	wire.History.mekugiHistory = &r.History
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return r, false, fmt.Errorf("decode replay record: %w", err)
	}
	if wire.History.ExecObservation != nil {
		r.History.ExecObservation = &wire.History.ExecObservation.execObservation
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return r, false, errors.New("trailing replay record data")
	}
	if (r.Version != 1 && r.Version != 2) || r.Workspace != workspace || r.CallID != callID || r.Commentary != commentary {
		return r, false, errors.New("replay record identity/version mismatch")
	}
	if err := s.restoreSnapshots(&r); err != nil {
		return r, false, err
	}
	// Consumers and immutable comparisons use the expanded version-1 facts.
	r.Version, r.Snapshots = 1, nil
	return r, true, nil
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
		return s.publishChanges(workspace, histories)
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
	name := replayRecordName(r.Workspace, r.CallID, r.Commentary)
	prefix := "call-"
	if r.Commentary {
		prefix = "commentary-"
	}
	return s.writeManagedFile(name, prefix+"pending-", data)
}
