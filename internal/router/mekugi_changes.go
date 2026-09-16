package router

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

	"github.com/yusing/mekugi"
)

// The index contains identities and application receipts, not another copy of
// scripts or diffs. Replay records remain immutable translation facts.
type changeIndex struct {
	Version   int
	Workspace string
	Streams   []changeStream
	Changes   map[string]trackedChange
}

type changeStream struct {
	Thread  string
	Next    int
	Retired int `json:",omitzero"`
}

type trackedChange struct {
	Correlation  string
	Calls        []trackedCall
	RetiredCalls int `json:",omitzero"`
}

type trackedCall struct {
	ID        string
	Confirmed bool
}

func changeNotice(id string) string {
	if id == "" {
		return ""
	}
	return "change " + id + "\n"
}

// The word-based IDs use a separate namespace; older indexes remain cleanup-only.
const changeIndexPrefix = "changes-v2-"

func changeIndexName(workspace string) string {
	return fmt.Sprintf("%s%x.json", changeIndexPrefix, sha256.Sum256([]byte(workspace)))
}

func (s *mekugiReplayStore) readChangeIndex(workspace string) (changeIndex, error) {
	index := changeIndex{Version: 1, Workspace: workspace, Changes: make(map[string]trackedChange)}
	path := filepath.Join(s.directory, changeIndexName(workspace))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
		return index, errors.New("invalid change index file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return index, err
	}
	if len(data) > maxReplayRecordBytes {
		return index, errors.New("change index exceeds capacity")
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, fmt.Errorf("decode change index: %w", err)
	}
	if index.Version != 1 || index.Workspace != workspace || index.Changes == nil {
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
		threads[stream.Thread] = true
		streams[changeStreamName(position)] = stream.Next
		counts[changeStreamName(position)] = stream.Retired
	}
	correlations := make(map[string]bool, len(index.Changes))
	calls := make(map[string]bool)
	for id, change := range index.Changes {
		stream, number, err := parseChangeID(id)
		if err != nil || number > streams[stream] || change.Correlation == "" || correlations[change.Correlation] || change.RetiredCalls < 0 {
			return errors.New("invalid change identity or stream membership")
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

func (s *mekugiReplayStore) writeChangeIndex(index changeIndex) (err error) {
	if err := s.retainFiles(changeIndexName(index.Workspace)); err != nil {
		return err
	}
	if err := s.reconcileRetiredChanges(&index); err != nil {
		return err
	}
	data, err := marshalProtocolJSON(index)
	if err != nil {
		return err
	}
	if err := s.maintainStorage(changeIndexName(index.Workspace), int64(len(data)), false, &index); err != nil {
		return err
	}
	// Quota cleanup may have retired older changes while this snapshot was
	// prepared. Keep stream counters, but do not restore deleted attempts.
	if err := s.reconcileRetiredChanges(&index); err != nil {
		return err
	}
	data, err = marshalProtocolJSON(index)
	if err != nil {
		return err
	}
	return storageIOError(s.writeFile(changeIndexName(index.Workspace), "changes-pending-", data))
}

// reserveChange runs before evaluation, including private direct application.
// A replay or recovery with the same correlation never allocates another ID.
func (s *mekugiReplayStore) reserveChange(ctx context.Context, workspace, thread, correlation string) (id string, err error) {
	if s == nil || correlation == "" {
		return "", nil
	}
	s = s.scoped(ctx)
	err = s.locked(ctx, func() error {
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		for existing, change := range index.Changes {
			if change.Correlation == correlation {
				id = existing
				return syncReplayDirectory(s.directory)
			}
		}
		stream := slices.IndexFunc(index.Streams, func(stream changeStream) bool { return stream.Thread == thread })
		if stream == -1 {
			stream = len(index.Streams)
			index.Streams = append(index.Streams, changeStream{Thread: thread})
		}
		index.Streams[stream].Next++
		id = changeHandle(changeStreamName(stream), index.Streams[stream].Next)
		if _, exists := index.Changes[id]; exists {
			return errors.New("change stream would overwrite an existing ID")
		}
		index.Changes[id] = trackedChange{Correlation: correlation}
		return s.writeChangeIndex(index)
	})
	return
}

func (t *mekugiResponseTransform) changeIDForAttempt(attempt mekugi.AttemptMetadata) (string, error) {
	store := t.proxy.replayStore
	if !attempt.Correction {
		return store.reserveChange(t.ctx, t.directory, t.shellThreadID, attempt.CorrelationID)
	}
	if store == nil || attempt.CorrelationID == "" {
		return "", nil
	}
	var id string
	err := store.locked(t.ctx, func() error {
		index, err := store.readChangeIndex(t.directory)
		if err != nil {
			return err
		}
		for candidate, change := range index.Changes {
			if change.Correlation == attempt.CorrelationID {
				id = candidate
				break
			}
		}
		return nil
	})
	return id, err
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
func (s *mekugiReplayStore) publishChanges(workspace string, histories map[string]mekugiHistory) error {
	tracked := make([]string, 0, len(histories))
	for id, history := range histories {
		if history.ChangeID != "" {
			tracked = append(tracked, id)
		}
	}
	if len(tracked) == 0 {
		return nil
	}
	slices.SortFunc(tracked, func(a, b string) int {
		return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
	})
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return err
	}
	updates := make(map[string][]trackedCall)
	changed := false
	for _, callID := range tracked {
		history := histories[callID]
		change, exists := index.Changes[history.ChangeID]
		if !exists || change.Correlation != history.CorrelationID {
			return errors.New("change identity does not match replay record")
		}
		if !slices.ContainsFunc(change.Calls, func(call trackedCall) bool { return call.ID == callID }) {
			change.Calls = append(change.Calls, trackedCall{ID: callID, Confirmed: history.Applied})
			updates[history.ChangeID] = append(updates[history.ChangeID], trackedCall{ID: callID, Confirmed: history.Applied})
			index.Changes[history.ChangeID] = change
			changed = true
		}
	}
	if !changed {
		return syncReplayDirectory(s.directory)
	}
	if err := s.writeChangeIndex(index); err != nil {
		return err
	}
	s.notifyLiveDiff(index, updates)
	return nil
}

// Confirmation is published only after the entire incoming history validates.
// It is evidence for review, not an alternate source of recovery/alias ancestry.
func (s *mekugiReplayStore) confirmChanges(ctx context.Context, workspace string, histories map[string]mekugiHistory) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	var confirmed []string
	for callID, history := range histories {
		// Old replay facts remain visible, but unsupported IDs have no entry
		// in the current review index. Do not revive them during confirmation.
		if _, _, err := parseChangeID(history.ChangeID); err == nil && history.confirmed {
			confirmed = append(confirmed, callID)
		}
	}
	if len(confirmed) == 0 {
		return nil
	}
	slices.SortFunc(confirmed, func(a, b string) int {
		return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
	})
	return s.locked(ctx, func() error {
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		updates := make(map[string][]trackedCall)
		changed := false
		for _, callID := range confirmed {
			history := histories[callID]
			change, exists := index.Changes[history.ChangeID]
			if !exists || change.Correlation != history.CorrelationID {
				return errors.New("confirmed change identity is missing or inconsistent")
			}
			if !slices.ContainsFunc(change.Calls, func(call trackedCall) bool { return call.ID == callID }) {
				change, err = s.repairChangeCall(workspace, history.ChangeID, callID, change)
				if err != nil {
					return err
				}
				changed = true
			}
			for i := range change.Calls {
				if change.Calls[i].ID == callID && !change.Calls[i].Confirmed {
					updates[history.ChangeID] = append(updates[history.ChangeID], trackedCall{ID: callID, Confirmed: true})
					change.Calls[i].Confirmed = true
					changed = true
				}
			}
			index.Changes[history.ChangeID] = change
		}
		if !changed {
			return syncReplayDirectory(s.directory)
		}
		if err := s.writeChangeIndex(index); err != nil {
			return err
		}
		s.notifyLiveDiff(index, updates)
		return nil
	})
}

// A replay record can survive an interrupted index publication. Restore only
// verified membership, positioning it by durable recovery-attempt order without
// reordering existing members (including independent recovery branches).
func (s *mekugiReplayStore) repairChangeCall(workspace, changeID, callID string, change trackedChange) (trackedChange, error) {
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
	change.Calls = slices.Insert(change.Calls, position, trackedCall{ID: callID})
	return change, nil
}

func changeHandle(stream string, number int) string {
	var name strings.Builder
	for _, letter := range stream {
		name.WriteString(handleWords[letter-'a'])
	}
	name.WriteString(strconv.Itoa(number))
	return name.String()
}

func parseChangeID(id string) (stream string, number int, err error) {
	tail := id
	for {
		matched := false
		for index, word := range handleWords[:26] {
			if rest, found := strings.CutPrefix(tail, word); found {
				stream += string(rune('a' + index))
				tail, matched = rest, true
				break
			}
		}
		if !matched {
			break
		}
	}
	number, err = strconv.Atoi(tail)
	if stream == "" || err != nil || number < 1 || strconv.Itoa(number) != tail {
		return "", 0, fmt.Errorf("invalid change ID %q", id)
	}
	return stream, number, nil
}

func expandChangeRefs(refs []string) ([]string, error) {
	const maxReadChanges = 256
	var ids []string
	seen := make(map[string]bool)
	for _, ref := range refs {
		start, end, ranged := strings.Cut(ref, "..")
		stream, first, err := parseChangeID(start)
		if err != nil {
			return nil, err
		}
		last := first
		if ranged {
			endStream, endNumber, err := parseChangeID(end)
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
			id := changeHandle(stream, first+offset)
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
