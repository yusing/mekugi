package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/commentaryid"
	"github.com/yusing/mekugi/internal/responses"
)

// Hook recovery matches only the latest response. Native history presentation
// retains exact answered items so later compactions cannot relabel older items.
type journalCompactionRecord struct {
	Version       int                     `json:"version"`
	Workspace     string                  `json:"workspace"`
	Thread        string                  `json:"thread"`
	ResponseID    string                  `json:"response_id"`
	AnsweredItems []journalCompactionItem `json:"answered_items,omitempty"`
}

type journalCompactionItem struct {
	Turn       string `json:"turn"`
	Item       string `json:"item"`
	ResponseID string `json:"response_id,omitempty"`
}

type journalCompactionRecovery struct {
	Workspace  string `json:"workspace"`
	Thread     string `json:"thread"`
	ResponseID string `json:"response_id"`
	Text       string `json:"text"`
	Namespace  string `json:"namespace,omitempty"`
	Reference  string `json:"reference,omitempty"`
}

const journalCompactionReferencePrefix = "mekugi:journal:"

func journalCompactionRecoveryName(workspace, thread, response string) string {
	return fmt.Sprintf("compaction-%x.json", sha256.Sum256([]byte("recovery\x00"+journalKey(workspace, thread)+"\x00"+response)))
}

func journalCompactionName(workspace, thread string) string {
	return fmt.Sprintf("compaction-%x.json", sha256.Sum256([]byte(journalKey(workspace, thread))))
}

// Compaction and execution-free requests skip ordinary replay projection, but
// must still resolve local native history before any provider sees it.
func (p *mekugiProxy) prepareJournalCompactionInput(ctx context.Context, request *parsedResponsesRequest, sessionID, threadID string, metadata codexTurnMetadata) error {
	items, local, err := journalCompactionInput(request)
	if err != nil || !local {
		return err
	}
	if p == nil || p.replayStore == nil || threadID == "" || metadata.activityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != threadID {
		return errors.New("journal compaction recovery identity or storage is unavailable")
	}
	workspace, ok := usableRoutingDirectory(metadata.Directories)
	if !ok && len(metadata.Directories) != 0 {
		return errors.New("journal compaction recovery workspace is unavailable")
	}
	ctx, release, err := p.replayStore.beginSession(ctx, threadID, sessionID)
	if err != nil {
		return err
	}
	defer release()
	if workspace == "" && metadata.RequestKind == responses.Compaction {
		err = p.replayStore.scoped(ctx).locked(ctx, func() error {
			workspace, err = p.replayStore.compactionWorkspace(threadID)
			return err
		})
	}
	if err != nil {
		return err
	}
	ctx, err = p.replayStore.prepareHandleScope(ctx, metadata)
	if err != nil {
		return err
	}
	// Validate visible replay before publishing any inherited handle scope.
	observed := *request
	observed.fields = maps.Clone(request.fields)
	if _, err := p.reconcileVisibleInput(ctx, &observed, workspace, workspace+"\x00"+threadID); err != nil {
		return err
	}
	return p.replayStore.restoreJournalCompactionItems(ctx, request, workspace, items)
}

func journalCompactionInput(request *parsedResponsesRequest) ([]map[string]jsonv1.RawMessage, bool, error) {
	raw := bytes.TrimSpace(request.fields["input"])
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false, nil
	}
	var items []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, err
	}
	for _, item := range items {
		if (jsonString(item, "type") == "compaction" || jsonString(item, "type") == "compaction_summary") && strings.HasPrefix(jsonString(item, "encrypted_content"), journalCompactionReferencePrefix) {
			return items, true, nil
		}
	}
	return items, false, nil
}

func (s *mekugiReplayStore) restoreJournalCompactionInput(ctx context.Context, request *parsedResponsesRequest, workspace string) error {
	items, local, err := journalCompactionInput(request)
	if err != nil || !local {
		return err
	}
	return s.restoreJournalCompactionItems(ctx, request, workspace, items)
}

func (s *mekugiReplayStore) restoreJournalCompactionItems(ctx context.Context, request *parsedResponsesRequest, workspace string, items []map[string]jsonv1.RawMessage) error {
	if s == nil {
		return errors.New("journal compaction recovery storage is unavailable")
	}
	s = s.scoped(ctx)
	err := s.locked(ctx, func() error {
		var names []string
		for index, item := range items {
			if jsonString(item, "type") != "compaction" && jsonString(item, "type") != "compaction_summary" {
				continue
			}
			reference := jsonString(item, "encrypted_content")
			if !strings.HasPrefix(reference, journalCompactionReferencePrefix) {
				continue // Provider-owned ciphertext has its native meaning.
			}
			parts := strings.Split(strings.TrimPrefix(reference, journalCompactionReferencePrefix), ":")
			if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
				return errors.New("invalid journal compaction reference")
			}
			owner, err := s.handleOwner(parts[0])
			if err != nil {
				return err
			}
			recoveryWorkspace := workspace
			if recoveryWorkspace == "" {
				// Authorization precedes source lookup. Recovery scope never
				// supplies the request's filesystem execution directory.
				recoveryWorkspace, err = s.compactionWorkspace(parts[1])
				if err != nil {
					return fmt.Errorf("resolve journal compaction recovery workspace: %w", err)
				}
			}
			name := journalCompactionRecoveryName(recoveryWorkspace, parts[1], parts[2])
			data, err := readManagedOutputFile(filepath.Join(s.directory, name))
			if err != nil {
				return fmt.Errorf("read journal compaction recovery: %w", err)
			}
			var recovery journalCompactionRecovery
			if err := json.Unmarshal(data, &recovery); err != nil {
				return err
			}
			if recovery.Workspace != recoveryWorkspace || recovery.Thread != parts[1] || recovery.ResponseID != parts[2] || recovery.Reference != reference || recovery.Namespace != owner || strings.TrimSpace(recovery.Text) == "" || len(recovery.Text) > maxJournalSummaryBytes {
				return errors.New("invalid journal compaction recovery identity or size")
			}
			items[index] = map[string]jsonv1.RawMessage{
				"type": mustMarshalJSON("message"), "role": mustMarshalJSON("assistant"),
				"content": mustMarshalJSON([]any{map[string]any{"type": "output_text", "text": recovery.Text, "annotations": []any{}}}),
			}
			names = append(names, name)
		}
		return s.retainFiles(names...)
	})
	if err != nil {
		return err
	}
	input, err := json.Marshal(&items)
	if err == nil {
		request.setInput(input)
	}
	return err
}

// Compaction shares ordinary terminal delivery but neither invokes a provider
// nor initializes model usage. Auto errors preserve context without a provider request.
func (a *requestAttempt) tryJournalCompaction() (bool, error) {
	if !a.metadataValid || a.metadata.RequestKind != responses.Compaction {
		return false, nil
	}
	p := a.executor.mekugiCalls
	if p == nil {
		return false, nil
	}
	if p.replayStore == nil || a.prewarm ||
		a.threadID == "" || a.metadata.activityIdentityInvalid ||
		a.metadata.ThreadID != "" && a.metadata.ThreadID != a.threadID ||
		slices.ContainsFunc(a.headers.Values(threadIDHeader), func(value string) bool {
			return strings.TrimSpace(value) != "" && strings.TrimSpace(value) != a.threadID
		}) ||
		len(a.metadata.Directories) > 1 {
		return a.journalCompactionUnavailable(errors.New("context reset identity or storage is unavailable"))
	}
	workspace, ok := usableRoutingDirectory(a.metadata.Directories)
	if len(a.metadata.Directories) != 0 && !ok {
		return a.journalCompactionUnavailable(errors.New("context reset workspace is unavailable"))
	}
	ctx, release, err := p.replayStore.beginSession(a.startCtx, a.threadID, a.sessionID)
	if err != nil {
		return a.journalCompactionUnavailable(err)
	}
	store := p.replayStore.scoped(ctx)
	if workspace == "" {
		err = store.locked(ctx, func() error {
			workspace, err = store.compactionWorkspace(a.threadID)
			return err
		})
	}
	if err == nil {
		ctx, err = store.prepareHandleScope(ctx, a.metadata)
		store = store.scoped(ctx)
	}
	if err == nil {
		// Both compaction policies consume these results. Reconcile on a
		// detached request: persist host outcomes, discard replay projections.
		observed := a.request
		observed.fields = maps.Clone(a.request.fields)
		_, err = p.reconcileVisibleInput(ctx, &observed, workspace, workspace+"\x00"+a.threadID)
	}
	if err != nil {
		release()
		return a.journalCompactionUnavailable(err)
	}
	if p.journalCompaction != "auto" && p.journalCompaction != "slice" {
		release()
		return false, nil
	}
	var summary journalSummary
	var wire []byte
	err = store.locked(ctx, func() error {
		j, exists, err := readThreadJournal(store, workspace, a.threadID)
		if err != nil {
			return err
		}
		if !exists {
			// A change-only session has no task state to restore. Durable
			// executing-thread ownership, not the request's prose, supplies it.
			j = threadJournal{Workspace: workspace, Thread: a.threadID, IdentityKnown: true}
		}
		var metadata struct {
			Trigger        string `json:"trigger"`
			Phase          string `json:"phase"`
			Implementation string `json:"implementation"`
		}
		_ = json.Unmarshal(a.metadata.Compaction, &metadata)
		// Compaction turns do not begin a journal turn, so an intent armed
		// after the completed slice still names the latest ordinary turn.
		reset := j.ResetIntent != nil && j.ResetIntent.Phase == "armed" && j.ResetIntent.Turn == j.TurnID &&
			metadata.Trigger == "manual" && metadata.Phase == "standalone_turn"
		if p.journalCompaction == "slice" && !reset {
			return errJournalUnchanged
		}
		summary, err = store.journalSummaryLocked(ctx, j)
		if err != nil {
			return err
		}
		if !exists && summary.Changes == 0 && summary.Failures == 0 {
			return errors.New("no durable compaction evidence")
		}
		id := "resp_mekugi_compact_" + rand.Text()
		recovery := journalCompactionRecovery{Workspace: workspace, Thread: a.threadID, ResponseID: id, Text: summary.Text}
		v2 := metadata.Implementation == "responses_compaction_v2"
		if v2 {
			handles, handleErr := store.allocateHandlesLocked(1)
			if handleErr != nil {
				return handleErr
			}
			recovery.Namespace = store.handleNamespace()
			recovery.Reference = journalCompactionReferencePrefix + handles[0] + ":" + a.threadID + ":" + id
			wire, err = journalCompactionItemSSE(id, a.request.model(), map[string]any{
				"id": commentaryid.CompactionPrefix + id, "type": "compaction", "encrypted_content": recovery.Reference,
			})
		} else {
			wire, err = journalCompactionSSE(id, a.request.model(), summary.Text)
		}
		if err != nil {
			return err
		}
		record := journalCompactionRecord{Version: 1, Workspace: workspace, Thread: a.threadID, ResponseID: id}
		// Presentation provenance is auxiliary: unavailable older receipts must
		// not stop an otherwise usable journal summary from replacing context.
		if previous, err := readManagedOutputFile(filepath.Join(store.directory, journalCompactionName(workspace, a.threadID))); err == nil {
			var prior journalCompactionRecord
			if json.Unmarshal(previous, &prior) == nil && prior.Version == 1 && prior.Workspace == workspace && prior.Thread == a.threadID {
				record.AnsweredItems = prior.AnsweredItems
			}
		}
		if a.metadata.TurnID != "" && metadata.Trigger == "manual" && metadata.Phase == "standalone_turn" {
			// A standalone manual turn owns one compaction lifecycle. Ordinary
			// turns may own several, and buffered UI events cannot identify which
			// item issued this HTTP request. Never infer that from the live sink.
			item := journalCompactionItem{Turn: a.metadata.TurnID, ResponseID: id}
			if !slices.Contains(record.AnsweredItems, item) {
				record.AnsweredItems = append(record.AnsweredItems, item)
			}
		}
		if v2 || a.metadata.TurnID != "" && metadata.Trigger == "manual" && metadata.Phase == "standalone_turn" {
			// Save the exact summary before exposing the native item. Automatic
			// V2 resets need this record even without presentation provenance.
			data, err := json.Marshal(&recovery)
			if err != nil {
				return err
			}
			if err := store.writeManagedFile(journalCompactionRecoveryName(workspace, a.threadID, id), "compaction-pending-", data); err != nil {
				return err
			}
		}
		data, err := json.Marshal(&record)
		if err != nil {
			return err
		}
		if err := store.writeManagedFile(journalCompactionName(workspace, a.threadID), "compaction-pending-", data); err != nil {
			return err
		}
		if reset {
			j.ResetIntent.Phase, j.ResetIntent.ResponseID = "consumed", id
			return writeThreadJournal(store, j)
		}
		return nil
	})
	if err != nil {
		release()
		if errors.Is(err, errJournalUnchanged) {
			return false, nil
		}
		return a.journalCompactionUnavailable(err)
	}
	a.compactionRelease = release
	a.response = &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(bytes.NewReader(wire)),
	}
	a.streamResponse = true
	a.finalization.upstreamStatusCode = http.StatusOK
	capturer.ObserveCompaction(a.startCtx, "router", len(summary.Text), summary.Changes, summary.Failures)
	return true, nil
}

// Only manual standalone receipts may bind a host item. Ordinary automatic
// compactions can share a turn, so their request-to-item identity remains unknown.
func (s *mekugiReplayStore) bindStandaloneCompactionItem(ctx context.Context, workspace, thread, turn, item string) {
	if turn == "" || item == "" {
		return
	}
	_ = s.locked(ctx, func() error {
		data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionName(workspace, thread)))
		if err != nil {
			return err
		}
		var record journalCompactionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Version != 1 || record.Workspace != workspace || record.Thread != thread {
			return nil
		}
		pending := slices.IndexFunc(record.AnsweredItems, func(entry journalCompactionItem) bool { return entry.Turn == turn && entry.Item == "" })
		if pending < 0 {
			return nil
		}
		record.AnsweredItems[pending].Item = item
		data, err = json.Marshal(&record)
		if err != nil {
			return err
		}
		return s.writeManagedFile(journalCompactionName(workspace, thread), "compaction-pending-", data)
	})
}

// Slice retains its provider fallback. Auto must never run provider compaction.
func (a *requestAttempt) journalCompactionUnavailable(err error) (bool, error) {
	if p := a.executor.mekugiCalls; p != nil && p.journalCompaction == "auto" {
		a.debug.event(map[string]any{
			"event": "journal_context_reset_failed", "error": err.Error(),
			"request_id": a.debugID, "session_id": a.sessionID, "thread_id": a.threadID,
		})
		return false, &requestCompatibilityError{code: "journal_context_reset_unavailable", message: "Context reset unavailable: " + err.Error()}
	}
	a.journalCompactionFallback(err)
	return false, nil
}

func (a *requestAttempt) journalCompactionFallback(err error) {
	a.debug.event(map[string]any{
		"event": "journal_compaction_fallback", "error": err.Error(),
		"request_id": a.debugID, "session_id": a.sessionID, "thread_id": a.threadID,
	})
	// Keep distinct causes visible while the native queue deduplicates retries.
	category := fmt.Sprintf("journal_compaction_fallback:%x", sha256.Sum256([]byte(err.Error())))
	a.executor.mekugiCalls.notice(a.sessionID, a.threadID, category,
		"Journal compaction unavailable; using the provider summary. Error: "+err.Error())
}

// Native local compaction omits workspaces from its metadata. In that case only
// the requesting thread's uniquely retained, previously selected workspace can
// supply the directory. Multiple historical workspaces are deliberately ambiguous.
// Called under the replay lock, after acquiring the requesting session lease.
func (s *mekugiReplayStore) compactionWorkspace(thread string) (string, error) {
	session, err := s.readRetainedSession(storageSessionName(thread))
	if err != nil {
		return "", err
	}
	workspace := ""
	for name := range session.Files {
		if !strings.HasPrefix(name, "journal-") && !strings.HasPrefix(name, "call-") {
			continue
		}
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if err != nil {
			return "", err
		}
		var candidate string
		if strings.HasPrefix(name, "journal-") {
			var j threadJournal
			if json.Unmarshal(data, &j) != nil || journalFilename(j.Workspace, j.Thread) != name {
				return "", errors.New("invalid journal workspace evidence")
			}
			if j.Thread == thread {
				if !j.IdentityKnown || j.IdentityConflicted {
					return "", errors.New("conflicted compaction identity")
				}
				candidate = j.Workspace
			}
		} else {
			var record replayRecord
			// Replay records retain v1 numeric byte arrays (command output hashes).
			if json.Unmarshal(data, &record, jsonv1.FormatByteArrayAsArray(true)) != nil || (record.Version != 1 && record.Version != 2) || replayRecordName(record.Workspace, record.CallID, record.Commentary) != name {
				return "", errors.New("invalid retained workspace evidence")
			}
			if record.History.ExecutingThread == thread {
				candidate = record.Workspace
			}
		}
		if candidate != "" {
			if workspace != "" && workspace != candidate {
				return "", errors.New("compaction workspace is ambiguous")
			}
			workspace = candidate
		}
	}
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return "", errors.New("no retained compaction workspace")
	}
	return workspace, nil
}

func journalCompactionSSE(id, model, summary string) ([]byte, error) {
	if strings.TrimSpace(summary) == "" {
		return nil, errors.New("empty journal compaction summary")
	}
	item := map[string]any{
		"id": commentaryid.CompactionPrefix + id, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": summary, "annotations": []any{}}},
	}
	return journalCompactionItemSSE(id, model, item)
}

func journalCompactionItemSSE(id, model string, item map[string]any) ([]byte, error) {
	response := map[string]any{
		"id": id, "object": "response", "model": model, "status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	}
	var wire bytes.Buffer
	for i, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "model": model, "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": response},
	} {
		event["sequence_number"] = i
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		if _, err := writeSSEEvent(&wire, responseSSELines(data, "\n"), "\n", nil, nil); err != nil {
			return nil, err
		}
	}
	return wire.Bytes(), nil
}

// Codex supplies no response ID in SessionStart. Its compacted rollout record
// carries the completed compaction_response_id. Read a bounded tail and fail
// open to ordinary hook recovery when the record is absent or unparseable.
func compactedResponseID(transcript string) string {
	if !filepath.IsAbs(transcript) {
		return ""
	}
	f, err := os.Open(transcript)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	const tailBytes = 4 << 20
	start := max(int64(0), info.Size()-tailBytes)
	data := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(data, start); err != nil {
		return ""
	}
	if start > 0 {
		_, data, _ = bytes.Cut(data, []byte{'\n'})
	}
	var responseID string
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				ResponseID string `json:"compaction_response_id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return ""
		}
		if row.Type == "compacted" {
			responseID = row.Payload.ResponseID
		}
	}
	return responseID
}

func (s *mekugiReplayStore) answeredCompaction(ctx context.Context, workspace, thread, responseID string) bool {
	if responseID == "" {
		return false
	}
	matched := false
	err := s.locked(ctx, func() error {
		data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionName(workspace, thread)))
		if err != nil {
			return err
		}
		var record journalCompactionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		matched = record.Version == 1 && record.Workspace == workspace && record.Thread == thread && record.ResponseID == responseID
		return nil
	})
	return err == nil && matched
}

// Missing exact item provenance (including older receipts) keeps host wording.
func (s *mekugiReplayStore) answeredCompactionItem(ctx context.Context, workspace, thread, turn, item string) bool {
	if turn == "" || item == "" {
		return false
	}
	matched := false
	err := s.locked(ctx, func() error {
		data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionName(workspace, thread)))
		if err != nil {
			return err
		}
		var record journalCompactionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		matched = record.Version == 1 && record.Workspace == workspace && record.Thread == thread && slices.ContainsFunc(record.AnsweredItems, func(entry journalCompactionItem) bool { return entry.Turn == turn && entry.Item == item })
		return nil
	})
	return err == nil && matched
}

// compactionRecovery reads only the original retained message. Slice journal
// events identify a standalone turn, so an empty item requires a unique receipt
// for that turn. Missing or ambiguous evidence never substitutes today's journal.
func (s *mekugiReplayStore) compactionRecovery(ctx context.Context, workspace, thread, turn, item string) (string, error) {
	if turn == "" {
		return "", nil
	}
	var text string
	err := s.locked(ctx, func() error {
		data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionName(workspace, thread)))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var record journalCompactionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Version != 1 || record.Workspace != workspace || record.Thread != thread {
			return errors.New("invalid compaction receipt identity")
		}
		if item == "" {
			matches := 0
			for _, entry := range record.AnsweredItems {
				if entry.Turn == turn {
					matches++
				}
			}
			if matches != 1 {
				return nil
			}
		}
		for _, entry := range record.AnsweredItems {
			if entry.Turn != turn || item != "" && entry.Item != item || entry.ResponseID == "" {
				continue
			}
			data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionRecoveryName(workspace, thread, entry.ResponseID)))
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			var recovery journalCompactionRecovery
			if err := json.Unmarshal(data, &recovery); err != nil {
				return err
			}
			if recovery.Workspace != workspace || recovery.Thread != thread || recovery.ResponseID != entry.ResponseID || len(recovery.Text) > maxJournalSummaryBytes {
				return errors.New("invalid compaction recovery identity or size")
			}
			text = recovery.Text
			return nil
		}
		return nil
	})
	return text, err
}
