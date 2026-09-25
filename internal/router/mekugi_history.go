package router

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi"
)

const (
	maxMekugiHistorySessionBytes = 32 << 20
	maxMekugiHistoryGlobalBytes  = 128 << 20
)

// mekugiHistory owns the version-1 replay fields and a request-local view.
// Exported field names are the durable JSON schema. Unexported fields are never
// serialized and durableHistory clears them before immutable comparisons.
type mekugiHistory struct {
	ToolName string
	PluginID string

	Script          string
	Root            string
	ExecutingThread string `json:",omitempty"`
	// Caller is the canonical agent path that issued the call; Source names
	// the edit's program when the tool name alone does not (exec_command).
	Caller          string `json:",omitempty"`
	Source          string `json:",omitempty"`
	ChangeID        string
	ReviewFiles     []mekugi.ReviewFile
	NativePatches   []nativePatchObservation `json:",omitempty"`
	ExecObservation *execObservation         `json:",omitempty"`
	ExecOutcome     *execOutcome             `json:",omitempty"`
	HostResults     []nativeToolResult       `json:",omitempty"`
	Applied         bool
	CarrierName     string
	CarrierKind     codeModeCarrierKind
	CarrierPayload  string

	Report string
	// Deferred diagnostics are projected onto the model-visible result, never
	// evaluated in a program that owns the host's output-helper identifier.
	JournalIDs           []string
	OutputWarning        string
	TranslationError     string
	CorrelationID        string
	Attempt              int
	UpstreamItem         map[string]json.RawMessage
	ReplayCarrier        bool
	CommentaryMessageIDs []string
	AlreadySatisfied     bool

	bytes      int
	confirmed  bool
	nativeCell *nativeTraceCell
	// sequence orders a request-visible view (or the bounded memory cache).
	// It is never durable: replay derives recovery order from the input.
	sequence uint64
}

// confirmsReport accepts only the exact retained report, either directly or in
// the matching host carrier's completed envelope. Never search arbitrary output
// for success prose: failed, yielded, truncated, or extra output is not a receipt.
func (h mekugiHistory) confirmsReport(raw json.RawMessage) bool {
	if h.TranslationError != "" || h.Report == "" {
		return false
	}
	texts := executionOutputTexts(raw)
	// Code Mode prepends its metadata as a separate content block before
	// text(report). Native exec_command instead returns one combined block.
	if len(texts) == 2 && h.CarrierName != nativeExecCommandToolName {
		state, _, body := codeModeExecutionHeader(texts[0])
		return state == "Script completed" && body == "" && texts[1] == h.Report
	}
	if len(texts) != 1 {
		return false
	}
	text := texts[0]
	if text == h.Report {
		return true
	}
	if h.CarrierName == nativeExecCommandToolName {
		state, body := nativeExecutionHeader(text)
		return state == "Process exited with code 0" && body == h.Report
	}
	state, _, body := codeModeExecutionHeader(text)
	return state == "Script completed" && body == h.Report
}

type mekugiHistorySession struct {
	calls map[string]mekugiHistory
	bytes int
	// nextSequence is the order to assign the session's next retained call.
	nextSequence uint64
	lastUsed     uint64
}

func (p *mekugiProxy) activateSession(sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("mekugi response proxy is closed")
	}
	p.activeSessions[sessionID]++
	if session := p.sessions[sessionID]; session != nil {
		p.touchSession(session)
	}
	return nil
}

func (p *mekugiProxy) deactivateSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeSessions[sessionID] <= 1 {
		delete(p.activeSessions, sessionID)
		return
	}
	p.activeSessions[sessionID]--
}

func (p *mekugiProxy) touchSession(session *mekugiHistorySession) {
	p.sessionSequence++
	session.lastUsed = p.sessionSequence
}

func (p *mekugiProxy) rememberBatch(sessionID string, histories map[string]mekugiHistory) error {
	if len(histories) == 0 {
		return nil
	}
	prepared := make(map[string]mekugiHistory, len(histories))
	for callID, history := range histories {
		encodedItem, err := marshalProtocolJSON(history.UpstreamItem)
		if err != nil {
			return fmt.Errorf("encode mekugi history item: %w", err)
		}
		history.bytes = len(sessionID) + len(callID) + len(history.ToolName) + len(history.PluginID) + len(history.Script) + len(history.Root) + len(history.CarrierKind) + len(history.CarrierName) + len(history.CarrierPayload) + len(history.Report) + len(history.OutputWarning) + len(history.TranslationError) + len(history.CorrelationID) + len(encodedItem)
		for _, patch := range history.NativePatches {
			history.bytes += len(patch.Input)
			for _, file := range patch.Files {
				history.bytes += len(file.BeforePath) + len(file.AfterPath) + len(file.Before) + len(file.Error)
			}
		}
		history.bytes += len(history.ChangeID) + len(history.ExecutingThread) + len(history.Caller) + len(history.Source)
		for _, file := range history.ReviewFiles {
			history.bytes += len(file.BeforePath) + len(file.AfterPath) + len(file.Diff)
		}
		for _, messageID := range history.CommentaryMessageIDs {
			history.bytes += len(messageID)
		}
		prepared[callID] = history
	}
	if len(prepared) > maxSessionTurns {
		return errors.New("mekugi history batch exceeds call capacity")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	existing := p.sessions[sessionID]
	calls := make(map[string]mekugiHistory, len(prepared))
	nextSequence := uint64(0)
	oldSessionBytes := 0
	sessionBytes := 0
	if existing != nil {
		calls = maps.Clone(existing.calls)
		nextSequence = existing.nextSequence
		oldSessionBytes = existing.bytes
		sessionBytes = existing.bytes
	}
	callIDs := slices.Collect(maps.Keys(prepared))
	slices.SortFunc(callIDs, func(first, second string) int {
		if order := cmp.Compare(prepared[first].sequence, prepared[second].sequence); order != 0 {
			return order
		}
		return strings.Compare(first, second)
	})
	protected := make(map[string]bool, len(prepared))
	for _, callID := range callIDs {
		history := prepared[callID]
		if previous, ok := calls[callID]; ok {
			sessionBytes -= previous.bytes
			history.sequence = previous.sequence
		} else {
			nextSequence++
			history.sequence = nextSequence
		}
		sessionBytes += history.bytes
		calls[callID] = history
		protected[callID] = true
	}

	for len(calls) > maxSessionTurns || sessionBytes > maxMekugiHistorySessionBytes {
		oldest, ok := oldestHistoryCall(calls, protected)
		if !ok {
			if len(calls) > maxSessionTurns {
				return errors.New("mekugi history call capacity reached")
			}
			return errors.New("mekugi history byte capacity reached")
		}
		sessionBytes -= calls[oldest].bytes
		delete(calls, oldest)
	}

	totalBytes := p.historyBytes - oldSessionBytes + sessionBytes
	sessionCount := len(p.sessions)
	if existing == nil {
		sessionCount++
	}
	type sessionCandidate struct {
		id       string
		lastUsed uint64
	}
	candidates := make([]sessionCandidate, 0, len(p.sessions))
	for id, session := range p.sessions {
		if id == sessionID || p.activeSessions[id] != 0 {
			continue
		}
		candidates = append(candidates, sessionCandidate{id: id, lastUsed: session.lastUsed})
	}
	slices.SortFunc(candidates, func(first, second sessionCandidate) int {
		if order := cmp.Compare(first.lastUsed, second.lastUsed); order != 0 {
			return order
		}
		return strings.Compare(first.id, second.id)
	})

	evicted := make([]string, 0)
	for sessionCount > maxSessionHistories || totalBytes > maxMekugiHistoryGlobalBytes {
		if len(evicted) == len(candidates) {
			if sessionCount > maxSessionHistories {
				return errors.New("mekugi history session capacity reached")
			}
			return errors.New("mekugi history byte capacity reached")
		}
		id := candidates[len(evicted)].id
		evicted = append(evicted, id)
		sessionCount--
		totalBytes -= p.sessions[id].bytes
	}
	for _, id := range evicted {
		delete(p.sessions, id)
	}

	if existing == nil {
		existing = &mekugiHistorySession{}
		p.sessions[sessionID] = existing
	}
	existing.calls = calls
	existing.bytes = sessionBytes
	existing.nextSequence = nextSequence
	p.touchSession(existing)
	p.historyBytes = totalBytes
	return nil
}

func oldestHistoryCall(histories map[string]mekugiHistory, protected map[string]bool) (string, bool) {
	oldestID := ""
	var oldest mekugiHistory
	found := false
	for callID, history := range histories {
		if protected[callID] {
			continue
		}
		if !found || history.sequence < oldest.sequence || history.sequence == oldest.sequence && callID < oldestID {
			oldestID = callID
			oldest = history
			found = true
		}
	}
	return oldestID, found
}

func (p *mekugiProxy) history(sessionID, callID string) (mekugiHistory, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	session := p.sessions[sessionID]
	if session == nil {
		return mekugiHistory{}, false
	}
	history, ok := session.calls[callID]
	return history, ok
}

// reconcileVisibleInput constructs recovery ancestry from the validated request,
// never from a routing session's most recent turn. All changes stay local until
// the entire input is valid, including output confirmations.
func (p *mekugiProxy) reconcileVisibleInput(ctx context.Context, request *parsedResponsesRequest, workspace, sessionID string) (map[string]mekugiHistory, error) {
	releaseSnapshot, err := p.replayStore.lockStorageSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseSnapshot()
	visible := make(map[string]mekugiHistory)
	raw, ok := request.fields["input"]
	if !ok {
		raw = json.RawMessage(`[]`)
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return visible, nil
	}
	changed := false
	commentaryIDs := p.commentaryMessageIDs(sessionID)
	filtered := make([]map[string]json.RawMessage, 0, len(items))
	removedCached := 0
	for index, item := range items {
		if jsonString(item, "type") == "message" {
			id := jsonString(item, "id")
			_, generated := commentaryIDs[id]
			if p.replayStore != nil {
				var err error
				generated, err = p.replayStore.hasCommentary(ctx, workspace, id)
				if err != nil {
					return nil, err
				}
			}
			if generated && index < request.cachedInput {
				removedCached++
			}
			if generated {
				changed = true
				continue
			}
		}
		filtered = append(filtered, item)
	}
	items = filtered
	validatedCarriers := make(map[string]bool)
	type completedNativePatch struct {
		callID  string
		history mekugiHistory
		output  json.RawMessage
	}
	var completedPatches []completedNativePatch
	pendingCalls := make(map[string]completedNativePatch)
	continuations := make(map[string]string)
	for index, item := range items {
		itemType := jsonString(item, "type")
		if itemType != "custom_tool_call" && itemType != "function_call" && itemType != "custom_tool_call_output" && itemType != "function_call_output" {
			continue
		}
		callID := jsonString(item, "call_id")
		if itemType == "function_call" {
			var args struct {
				CellID    string          `json:"cell_id"`
				SessionID json.RawMessage `json:"session_id"`
			}
			switch strings.TrimPrefix(jsonString(item, "name"), "functions.") {
			case "wait":
				if json.Unmarshal([]byte(jsonString(item, "arguments")), &args) == nil && args.CellID != "" {
					continuations[callID] = "cell:" + args.CellID
				}
			case "write_stdin":
				if json.Unmarshal([]byte(jsonString(item, "arguments")), &args) == nil && len(args.SessionID) != 0 {
					continuations[callID] = "session:" + strings.Trim(string(args.SessionID), `"`)
				}
			}
		}
		if itemType == "function_call_output" {
			if key := continuations[callID]; key != "" {
				if pending, found := pendingCalls[key]; found {
					// A continuation completes the original call, and its
					// result is read with the original call's state rules.
					resultTool := pending.history.ToolName
					if strings.HasPrefix(key, "session:") {
						resultTool = "write_stdin"
					}
					if terminal, _, _, _, _ := execResultState(resultTool, item["output"]); terminal {
						pending.output = bytes.Clone(item["output"])
						completedPatches = append(completedPatches, pending)
						delete(pendingCalls, key)
					}
				}
			}
		}
		journalResult := callID == "" && journalResultCallID(item) != ""
		if journalResult {
			callID = journalResultCallID(item)
		}
		history, known := visible[callID]
		if !known {
			if p.replayStore != nil {
				var err error
				history, known, err = p.replayStore.lookup(ctx, workspace, callID)
				if err != nil {
					return nil, err
				}
			} else {
				history, known = p.history(sessionID, callID)
			}
			if !known {
				continue
			}
			history.sequence = uint64(len(visible) + 1)
			history.confirmed = false
		}
		if journalResult && history.ToolName != journalHistoryTool && history.ToolName != reportIssueHistoryTool {
			continue
		}
		carrierKind := history.effectiveCarrierKind()
		if itemType == carrierOutputItemType(carrierKind) {
			if len(history.NativePatches) != 0 || history.ExecObservation != nil {
				patch := completedNativePatch{callID: callID, history: history, output: bytes.Clone(item["output"])}
				if terminal, _, _, _, pending := execResultState(history.ToolName, item["output"]); terminal {
					completedPatches = append(completedPatches, patch)
				} else if pending != "" {
					pendingCalls[pending] = patch
					p.execWindows.setSession(callID, pending)
				}
			}
			if history.confirmsReport(item["output"]) {
				history.confirmed = true
			}
			visible[callID] = history
			if len(history.JournalIDs) != 0 {
				projected, _, err := appendToolOutputWarning(item["output"], "Journal item IDs: "+strings.Join(history.JournalIDs, ", "))
				if err != nil {
					return nil, err
				}
				item["output"] = projected
			}
			if history.OutputWarning != "" {
				output, projected, err := appendToolOutputWarning(item["output"], history.OutputWarning)
				if err != nil {
					return nil, fmt.Errorf("project call %q warning: %w", callID, err)
				}
				if projected {
					item["output"] = output
					changed = true
				}
			}
			if !history.ReplayCarrier {
				upstreamKind := codeModeCarrierCustom
				if jsonString(history.UpstreamItem, "type") == carrierItemType(codeModeCarrierFunction) {
					upstreamKind = codeModeCarrierFunction
				}
				item["type"] = mustMarshalJSON(carrierOutputItemType(upstreamKind))
				changed = true
			}
			continue
		}
		if itemType != carrierItemType(carrierKind) {
			return nil, fmt.Errorf("replayed call %q changed item type", callID)
		}
		if jsonString(item, "name") != history.CarrierName {
			return nil, fmt.Errorf("replayed call %q changed carrier name", callID)
		}
		if validatedCarriers[callID] {
			return nil, fmt.Errorf("replayed call %q appears more than once", callID)
		}
		if jsonString(item, carrierPayloadField(carrierKind)) != history.carrierInput() {
			return nil, fmt.Errorf("replayed call %q changed translated payload", callID)
		}
		validatedCarriers[callID] = true
		visible[callID] = history
		if history.ReplayCarrier {
			continue
		}
		if len(history.UpstreamItem) != 0 {
			items[index] = maps.Clone(history.UpstreamItem)
		} else {
			item["name"] = mustMarshalJSON(cmp.Or(history.ToolName, applyPatchToolName))
			item["input"] = mustMarshalJSON(history.Script)
		}
		changed = true
	}
	var encoded json.RawMessage
	if changed {
		var err error
		encoded, err = marshalProtocolJSON(items)
		if err != nil {
			return nil, fmt.Errorf("encode replayed Responses input: %w", err)
		}
	}
	if err := p.replayStore.retainInput(ctx, workspace, raw, visible, releaseSnapshot); err != nil {
		return nil, err
	}
	releaseSnapshot()
	// The native trace supplies per-tool outcomes even when JavaScript discards
	// or catches a nested result. Never infer them from text printed by the cell.
	ready := completedPatches[:0]
	for _, completed := range completedPatches {
		if completed.history.ToolName != applyPatchToolName && completed.history.ToolName != nativeExecCommandToolName {
			source := completed.history.CarrierPayload
			if source == "" {
				source = completed.history.Script
			}
			completed.history.nativeCell = p.nativeTrace.readCell(completed.history.ExecutingThread, completed.callID, source)
			if completed.history.nativeCell.pending() {
				continue
			}
		}
		ready = append(ready, completed)
	}
	completedPatches = ready
	execGroups := make(map[string][]execCompletion)
	for _, completed := range completedPatches {
		key := execSiblingKey(completed.history)
		if key == "" {
			key = completed.callID
		}
		execGroups[key] = append(execGroups[key], execCompletion(completed))
	}
	for _, completed := range completedPatches {
		thread := completed.history.ExecutingThread
		if err := p.finalizeNativePatches(ctx, workspace, thread, completed.callID, completed.history, completed.output); err != nil {
			return nil, err
		}
		if completed.history.ExecObservation == nil {
			p.execWindows.close(completed.callID)
		}
		key := execSiblingKey(completed.history)
		if key == "" {
			key = completed.callID
		}
		if members := execGroups[key]; len(members) != 0 {
			if err := p.finalizeExecObservations(ctx, workspace, members); err != nil {
				return nil, err
			}
			delete(execGroups, key)
		}
	}
	if err := p.replayStore.confirmChanges(ctx, workspace, visible); err != nil {
		return nil, err
	}
	request.cachedInput -= removedCached
	if changed {
		request.setInput(encoded)
	}
	return visible, nil
}

func appendToolOutputWarning(raw json.RawMessage, warning string) (json.RawMessage, bool, error) {
	var parts []json.RawMessage
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parts = []json.RawMessage{mustMarshalJSON(map[string]string{"type": "input_text", "text": text})}
	} else if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, false, errors.New("tool output must be text or content parts")
	}
	notice := mustMarshalJSON(map[string]string{"type": "input_text", "text": warning})
	for _, part := range parts {
		if sameJSONValue(part, notice) {
			return raw, false, nil
		}
	}
	return mustMarshalJSON(append(parts, notice)), true, nil
}

func (t *mekugiResponseTransform) recordLocal(callID string, history *mekugiHistory) {
	t.featureTrace.toolCall(callID, history.ToolName)
	t.localSequence++
	history.sequence = t.localSequence
	t.local[callID] = *history
}

func (t *mekugiResponseTransform) commitHistory() error {
	if t.historyCommitted {
		return nil
	}
	if err := t.proxy.replayStore.put(t.ctx, t.directory, t.local); err != nil {
		return criticalDiagnostic(err, "replay_history_commit", "Mekugi could not persist completed response history", true)
	}
	if err := t.proxy.rememberBatch(t.historySessionID, t.local); err != nil && t.proxy.replayStore == nil {
		return criticalDiagnostic(err, "replay_history_commit", "Mekugi could not retain completed response history", true)
	}
	t.historyCommitted = true
	for callID := range t.local {
		t.handOffCommentary(callID)
	}
	return nil
}

func (t *mekugiResponseTransform) commitLocalCall(callID string) error {
	history, exists := t.local[callID]
	if !exists {
		return nil
	}
	if err := t.proxy.replayStore.put(t.ctx, t.directory, map[string]mekugiHistory{callID: history}); err != nil {
		return criticalDiagnostic(err, "replay_call_commit", "Mekugi could not persist a completed tool call", true)
	}
	if err := t.proxy.rememberBatch(t.historySessionID, map[string]mekugiHistory{callID: history}); err != nil && t.proxy.replayStore == nil {
		return criticalDiagnostic(err, "replay_call_commit", "Mekugi could not retain a completed tool call", true)
	}
	t.handOffCommentary(callID)
	return nil
}
