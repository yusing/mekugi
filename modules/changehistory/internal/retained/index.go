package retained

// Source: internal/router/mekugi_changes.go@[1a1981ba] retained change behavior.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// The index contains identities and application receipts, not another copy of
// scripts or diffs. Replay records remain immutable translation facts.
type changeIndex struct {
	// Transient stream ownership for namespace-wide command selections.
	workspaces   map[string]string
	discoveryErr error

	Sequence  uint64 `json:",omitzero"`
	Version   int
	Workspace string
	Namespace string `json:",omitzero"`
	Streams   []changeStream
	Changes   map[string]trackedChange
}

type changeStream struct {
	Name    string `json:",omitzero"`
	Thread  string
	Next    int
	Retired int `json:",omitzero"`
}

type trackedChange struct {
	Correlation  string
	Calls        []trackedCall
	Dependencies []string `json:",omitzero"`
	RetiredCalls int      `json:",omitzero"`
}

type trackedCall struct {
	Sequence uint64 `json:",omitzero"`
	Thread   string `json:",omitempty"` // Empty uses the originating change stream.
	ID       string
	Managed  bool `json:",omitzero"`
}

func changeNotice(id string) string {
	if id == "" {
		return ""
	}
	return "change " + id + "\n"
}

const changeIndexPrefix = "changes-v3-"

func changeIndexName(workspace, namespace string) string {
	key := namespace + "\x00" + workspace
	return fmt.Sprintf("%s%x.json", changeIndexPrefix, sha256.Sum256([]byte(key)))
}

func (s *store) readChangeIndex(workspace string) (changeIndex, error) {
	namespace := s.handleNamespace()
	index := changeIndex{Version: 1, Workspace: workspace, Namespace: namespace, Changes: make(map[string]trackedChange)}
	path := filepath.Join(s.directory, changeIndexName(workspace, namespace))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, err
	}
	if !info.Mode().IsRegular() || info.Size() > history.MaxRecordBytes {
		return index, errors.New("invalid change index file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return index, err
	}
	if len(data) > history.MaxRecordBytes {
		return index, errors.New("change index exceeds capacity")
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, fmt.Errorf("decode change index: %w", err)
	}
	if index.Version != 1 || index.Workspace != workspace || index.Namespace != namespace || index.Changes == nil {
		return index, errors.New("change index identity/version mismatch")
	}
	if err := validateChangeIndex(index); err != nil {
		return index, err
	}
	return index, nil
}

func validateChangeIndex(index changeIndex) error {
	streams := make(map[string]int, len(index.Streams))
	threads := make(map[string]bool, len(index.Streams))
	counts := make(map[string]int, len(index.Streams))
	for position, stream := range index.Streams {
		if stream.Next < 1 || stream.Retired < 0 || stream.Retired > stream.Next || threads[stream.Thread] {
			return errors.New("invalid change stream counter or duplicate thread")
		}
		name := index.streamName(position)
		if _, err := changeStreamOrdinal(name); err != nil || streams[name] != 0 {
			return errors.New("invalid or duplicate change stream name")
		}
		threads[stream.Thread] = true
		streams[index.streamName(position)] = stream.Next
		counts[index.streamName(position)] = stream.Retired
	}
	correlations := make(map[string]bool, len(index.Changes))
	calls := make(map[string]bool)
	for id, change := range index.Changes {
		stream, number, err := changes.ParseID(id)
		if err != nil || number > streams[stream] || change.Correlation == "" || correlations[change.Correlation] || change.RetiredCalls < 0 {
			return errors.New("invalid change identity or stream membership")
		}
		dependencies := make(map[string]bool, len(change.Dependencies))
		for _, id := range change.Dependencies {
			if id == "" || dependencies[id] {
				return errors.New("invalid or duplicated change dependency")
			}
			dependencies[id] = true
		}
		counts[stream]++
		correlations[change.Correlation] = true
		for _, call := range change.Calls {
			if call.ID == "" || calls[call.ID] {
				return errors.New("invalid or duplicated change attempt")
			}
			calls[call.ID] = true
		}
	}
	for stream, next := range streams {
		if counts[stream] != next {
			return errors.New("change stream counter does not match its IDs")
		}
	}
	return nil
}

// writeChangeIndex runs under store.lock, including retention and publication.
func (s *store) writeChangeIndex(index changeIndex) (err error) {
	if err := s.retainFiles(changeIndexName(index.Workspace, index.Namespace)); err != nil {
		return err
	}
	if _, err := s.reconcileRetiredChanges(&index); err != nil {
		return err
	}
	data, err := marshalIndex(index)
	if err != nil {
		return err
	}
	if err := s.maintainStorage(changeIndexName(index.Workspace, index.Namespace), int64(len(data))); err != nil {
		return err
	}
	return publicationError(s.transaction.Write(changeIndexName(index.Workspace, index.Namespace), "changes-pending-", data))
}

// reserveChange runs before host evaluation.
// A replay or recovery with the same correlation never allocates another ID.
func (s *store) reserveChange(ctx context.Context, workspace, thread, correlation string) (id string, err error) {
	if s == nil || correlation == "" {
		return "", nil
	}
	err = s.locked(ctx, func(s *store) error {
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		for existing, change := range index.Changes {
			if change.Correlation == correlation {
				id = existing
				return nil
			}
		}
		stream := slices.IndexFunc(index.Streams, func(stream changeStream) bool { return stream.Thread == thread })
		if stream == -1 {
			stream = len(index.Streams)
			name, err := s.allocateChangeStream()
			if err != nil {
				return err
			}
			index.Streams = append(index.Streams, changeStream{Name: name, Thread: thread})
		}
		index.Streams[stream].Next++
		id = changes.ID(index.streamName(stream), index.Streams[stream].Next)
		if _, exists := index.Changes[id]; exists {
			return errors.New("change stream would overwrite an existing ID")
		}
		index.Changes[id] = trackedChange{Correlation: correlation}
		return s.writeChangeIndex(index)
	})
	return
}

// streamName preserves positional names in older indexes and frozen read snapshots.
func (index changeIndex) streamName(position int) string {
	return cmp.Or(index.Streams[position].Name, changeStreamName(position))
}

func changeStreamName(index int) string {
	name := ""
	for index++; index > 0; index = (index - 1) / 26 {
		name = string(rune('a'+(index-1)%26)) + name
	}
	return name
}

// publishChanges is called under the replay lock, only after the corresponding
// immutable records are durable and before exposing their executor carriers.
func (s *store) publishChanges(workspace string, histories map[string]changes.Publication) error {
	tracked := make([]string, 0, len(histories))
	for id, history := range histories {
		if history.ChangeID != "" || history.NoEffect {
			tracked = append(tracked, id)
		}
	}
	if len(tracked) == 0 {
		return nil
	}
	slices.SortFunc(tracked, func(a, b string) int {
		return cmp.Or(cmp.Compare(histories[a].Sequence, histories[b].Sequence), strings.Compare(a, b))
	})
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return err
	}
	updates := make(map[string][]trackedCall)
	changed := false
	for _, callID := range tracked {
		history := histories[callID]
		if history.NoEffect {
			changed = retireEmptyReservation(&index, history.CorrelationID) || changed
			continue
		}
		change, exists := index.Changes[history.ChangeID]
		if !exists || change.Correlation != history.CorrelationID {
			return errors.New("change identity does not match replay record")
		}
		if history.Dependency {
			if !slices.Contains(change.Dependencies, callID) {
				change.Dependencies = append(change.Dependencies, callID)
				index.Changes[history.ChangeID] = change
				changed = true
			}
			continue
		}
		if !slices.ContainsFunc(change.Calls, func(call trackedCall) bool { return call.ID == callID }) {
			managed := history.Managed
			if index.Sequence == ^uint64(0) {
				return errors.New("change sequence exhausted")
			}
			index.Sequence++
			call := trackedCall{Sequence: index.Sequence, ID: callID, Thread: history.ExecutingThread, Managed: managed}
			change.Calls = append(change.Calls, call)
			updates[history.ChangeID] = append(updates[history.ChangeID], call)
			index.Changes[history.ChangeID] = change
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := s.writeChangeIndex(index); err != nil {
		return err
	}
	*s.updates = append(*s.updates, publicationUpdates(index, updates)...)
	return nil
}

// Repair durable change membership only after the incoming history validates.
// A matching tool report does not change a saved filesystem diff.
func (s *store) repairVisibleChangeCalls(ctx context.Context, workspace string, histories map[string]changes.Publication) error {
	if s == nil {
		return nil
	}
	var confirmed []string
	for callID, history := range histories {
		// Old replay facts remain visible, but unsupported IDs have no entry
		// in the current review index. Do not revive them during index repair.
		if _, _, err := changes.ParseID(history.ChangeID); (err == nil || history.NoEffect) && history.Confirmed {
			confirmed = append(confirmed, callID)
		}
	}
	if len(confirmed) == 0 {
		return nil
	}
	slices.SortFunc(confirmed, func(a, b string) int {
		return cmp.Or(cmp.Compare(histories[a].Sequence, histories[b].Sequence), strings.Compare(a, b))
	})
	return s.locked(ctx, func(s *store) error {
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		updates := make(map[string][]trackedCall)
		changed := false
		for _, callID := range confirmed {
			history := histories[callID]
			if history.NoEffect {
				record, found, err := s.read(workspace, callID, false)
				if err != nil {
					return err
				}
				if !found || record.History.CorrelationID != history.CorrelationID || record.History.ChangeID != "" || len(record.History.ReviewFiles) != 0 || record.History.ExecOutcome == nil || record.History.ExecOutcome.Coverage != "exact" {
					return errors.New("no-effect completion is missing or inconsistent")
				}
				changed = retireEmptyReservation(&index, history.CorrelationID) || changed
				continue
			}
			change, exists := index.Changes[history.ChangeID]
			if !exists || change.Correlation != history.CorrelationID {
				return errors.New("visible change identity is missing or inconsistent")
			}
			if !slices.ContainsFunc(change.Calls, func(call trackedCall) bool { return call.ID == callID }) {
				change, err = s.repairChangeCall(workspace, history.ChangeID, callID, change)
				if err != nil {
					return err
				}
				if index.Sequence == ^uint64(0) {
					return errors.New("change sequence exhausted")
				}
				index.Sequence++
				for i := range change.Calls {
					if change.Calls[i].ID == callID {
						change.Calls[i].Sequence = index.Sequence
					}
				}
				updates[history.ChangeID] = change.Calls
				changed = true
			}
			index.Changes[history.ChangeID] = change
		}
		if !changed {
			return nil
		}
		if err := s.writeChangeIndex(index); err != nil {
			return err
		}
		*s.updates = append(*s.updates, publicationUpdates(index, updates)...)
		return nil
	})
}

// A replay record can survive an interrupted index publication. Restore only
// verified membership, positioning it by durable recovery-attempt order without
// reordering existing members (including independent recovery branches).
func (s *store) repairChangeCall(workspace, changeID, callID string, change trackedChange) (trackedChange, error) {
	record, found, err := s.read(workspace, callID, false)
	if err != nil {
		return change, err
	}
	if !found || record.History.ChangeID != changeID || record.History.CorrelationID != change.Correlation ||
		record.History.Attempt < 1 || record.History.TranslationError != "" {
		return change, errors.New("cannot repair confirmed change from its durable replay record")
	}
	position := len(change.Calls)
	for i, call := range change.Calls {
		existing, found, err := s.read(workspace, call.ID, false)
		if err != nil {
			return change, err
		}
		if !found || existing.History.ChangeID != changeID || existing.History.CorrelationID != change.Correlation ||
			existing.History.Attempt < 1 {
			return change, errors.New("cannot determine repaired change attempt order")
		}
		if existing.History.Attempt > record.History.Attempt {
			position = i
			break
		}
	}
	change.Calls = slices.Insert(change.Calls, position, trackedCall{ID: callID, Thread: record.History.ExecutingThread})
	return change, nil
}

func expandChangeRefs(refs []string) ([]string, error) {
	const maxReadChanges = 256
	var ids []string
	seen := make(map[string]bool)
	for _, ref := range refs {
		start, end, ranged := strings.Cut(ref, "..")
		stream, first, err := changes.ParseID(start)
		if err != nil {
			return nil, err
		}
		last := first
		if ranged {
			if number, err := strconv.Atoi(end); err == nil && number > 0 && strconv.Itoa(number) == end {
				end = changes.ID(stream, number)
			}
			endStream, endNumber, err := changes.ParseID(end)
			if err != nil {
				return nil, err
			}
			if endStream != stream || endNumber < first {
				return nil, errors.New("change ranges must be ordered and within one agent stream")
			}
			last = endNumber
		}
		if last-first >= maxReadChanges {
			return nil, fmt.Errorf("read at most %d changes at once", maxReadChanges)
		}
		for offset := 0; offset <= last-first; offset++ {
			id := changes.ID(stream, first+offset)
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > maxReadChanges {
			return nil, fmt.Errorf("read at most %d changes at once", maxReadChanges)
		}
	}
	return ids, nil
}

// A witnessed boundary can reserve an identity before the writer has effects.
// Its terminal no-effect fact releases that reservation, never a completed edit.
func retireEmptyReservation(index *changeIndex, correlation string) bool {
	for id, change := range index.Changes {
		if change.Correlation != correlation || len(change.Calls) != 0 || len(change.Dependencies) == 0 {
			continue
		}
		delete(index.Changes, id)
		stream, _, _ := changes.ParseID(id)
		for i := range index.Streams {
			if index.streamName(i) == stream {
				index.Streams[i].Retired++
				break
			}
		}
		return true
	}
	return false
}
