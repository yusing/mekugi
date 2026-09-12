package router

import (
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

type mekugiHistory struct {
	toolName string
	pluginID string

	script string
	root   string
	// evaluated is the script mekugi actually received when it differs from the
	// model's payload, which happens when the payload was a recovery edit. Replay
	// must restore what the model emitted, while a following recovery must target
	// the script that produced the latest diagnostic.
	evaluated      string
	changeID       string
	reviewFiles    []mekugi.ReviewFile
	patch          string
	applied        bool
	carrierName    string
	carrierKind    codeModeCarrierKind
	carrierPayload string

	report string
	// Deferred diagnostics are projected onto the model-visible result, never
	// evaluated in a program that owns the host's output-helper identifier.
	journalIDs           []string
	outputWarning        string
	translationError     string
	evaluatorRejected    bool
	rejections           []mekugi.HostRejection
	correlationID        string
	attempt              int
	upstreamItem         map[string]json.RawMessage
	replayCarrier        bool
	commentaryMessageIDs []string
	bytes                int
	// unevaluated marks a call the proxy rejected before mekugi saw it. Such a
	// recovery changed nothing and has no script of its own, so another recovery
	// looks past it to the rejected script it was trying to repair.
	unevaluated      bool
	alreadySatisfied bool
	confirmed        bool
	aliases          []mekugi.TargetAlias
	// sequence orders a request-visible view (or the bounded memory cache).
	// It is never durable: replay derives recovery order from the input.
	sequence uint64
}

// confirmsReport accepts only the exact retained report, either directly or in
// the matching host carrier's completed envelope. Never search arbitrary output
// for success prose: failed, yielded, truncated, or extra output is not a receipt.
func (h mekugiHistory) confirmsReport(raw json.RawMessage) bool {
	if h.translationError != "" || h.report == "" {
		return false
	}
	texts := executionOutputTexts(raw)
	// Code Mode prepends its metadata as a separate content block before
	// text(report). Native exec_command instead returns one combined block.
	if len(texts) == 2 && h.carrierName != nativeExecCommandToolName {
		state, _, body := codeModeExecutionHeader(texts[0])
		return state == "Script completed" && body == "" && texts[1] == h.report
	}
	if len(texts) != 1 {
		return false
	}
	text := texts[0]
	if text == h.report {
		return true
	}
	if h.carrierName == nativeExecCommandToolName {
		state, body := nativeExecutionHeader(text)
		return state == "Process exited with code 0" && body == h.report
	}
	state, _, body := codeModeExecutionHeader(text)
	return state == "Script completed" && body == h.report
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
		encodedItem, err := marshalProtocolJSON(history.upstreamItem)
		if err != nil {
			return fmt.Errorf("encode mekugi history item: %w", err)
		}
		history.bytes = len(sessionID) + len(callID) + len(history.toolName) + len(history.pluginID) + len(history.script) + len(history.root) + len(history.evaluated) + len(history.patch) + len(history.carrierKind) + len(history.carrierName) + len(history.carrierPayload) + len(history.report) + len(history.outputWarning) + len(history.translationError) + len(history.correlationID) + len(encodedItem)
		history.bytes += len(history.changeID)
		for _, file := range history.reviewFiles {
			history.bytes += len(file.BeforePath) + len(file.AfterPath) + len(file.Diff)
		}
		for _, rejection := range history.rejections {
			history.bytes += mekugiRejectionTextBytes(rejection)
		}
		for _, alias := range history.aliases {
			history.bytes += len(alias.Path) + len(alias.Before) + len(alias.After)
		}
		for _, messageID := range history.commentaryMessageIDs {
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

func mekugiRejectionTextBytes(rejection mekugi.HostRejection) int {
	return len(rejection.Operation) + len(rejection.Target) + len(rejection.TargetAliasRelation) +
		len(rejection.Reason) + len(rejection.Path)
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
	visible := make(map[string]mekugiHistory)
	raw, ok := request.fields["input"]
	if !ok {
		return visible, nil
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
	request.cachedInput -= removedCached
	items = filtered
	validatedCarriers := make(map[string]bool)
	for index, item := range items {
		itemType := jsonString(item, "type")
		if itemType != "custom_tool_call" && itemType != "function_call" && itemType != "custom_tool_call_output" && itemType != "function_call_output" {
			continue
		}
		callID := jsonString(item, "call_id")
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
		if journalResult && history.toolName != journalHistoryTool {
			continue
		}
		carrierKind := history.effectiveCarrierKind()
		if itemType == carrierOutputItemType(carrierKind) {
			if history.confirmsReport(item["output"]) {
				history.confirmed = true
			}
			visible[callID] = history
			if len(history.journalIDs) != 0 {
				projected, _, err := appendToolOutputWarning(item["output"], "Journal item IDs: "+strings.Join(history.journalIDs, ", "))
				if err != nil {
					return nil, err
				}
				item["output"] = projected
			}
			if history.outputWarning != "" {
				output, projected, err := appendToolOutputWarning(item["output"], history.outputWarning)
				if err != nil {
					return nil, fmt.Errorf("project call %q warning: %w", callID, err)
				}
				if projected {
					item["output"] = output
					changed = true
				}
			}
			if !history.replayCarrier {
				upstreamKind := codeModeCarrierCustom
				if jsonString(history.upstreamItem, "type") == carrierItemType(codeModeCarrierFunction) {
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
		if jsonString(item, "name") != history.carrierName {
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
		if history.replayCarrier {
			continue
		}
		if len(history.upstreamItem) != 0 {
			items[index] = maps.Clone(history.upstreamItem)
		} else {
			item["name"] = mustMarshalJSON(cmp.Or(history.toolName, mekugiToolName))
			item["input"] = mustMarshalJSON(history.script)
		}
		changed = true
	}
	if changed {
		encoded, err := marshalProtocolJSON(items)
		if err != nil {
			return nil, fmt.Errorf("encode replayed Responses input: %w", err)
		}
		request.setInput(encoded)
	}
	if err := p.replayStore.confirmChanges(ctx, workspace, visible); err != nil {
		return nil, err
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
	t.featureTrace.toolCall(callID, history.toolName)
	if t.nativeTools && history.carrierKind == "" {
		history.carrierKind = codeModeCarrierFunction
		history.carrierName = nativeExecCommandToolName
		history.carrierPayload = renderExecCarrier(
			codeModeCarrierFunction,
			execCommandArguments(mekugiNativeCommand(*history), nil),
			false,
			nil,
		)
	}
	t.localSequence++
	history.sequence = t.localSequence
	t.local[callID] = *history
}

func (t *mekugiResponseTransform) commitHistory() error {
	if t.historyCommitted {
		return nil
	}
	if err := t.proxy.replayStore.put(t.ctx, t.directory, t.local); err != nil {
		return err
	}
	if err := t.proxy.rememberBatch(t.historySessionID, t.local); err != nil && t.proxy.replayStore == nil {
		return err
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
		return err
	}
	if err := t.proxy.rememberBatch(t.historySessionID, map[string]mekugiHistory{callID: history}); err != nil && t.proxy.replayStore == nil {
		return err
	}
	t.handOffCommentary(callID)
	return nil
}

// targetAliases includes only visible ancestry and calls applied in this turn.
func (t *mekugiResponseTransform) targetAliases() []mekugi.TargetAlias {
	histories := slices.Collect(maps.Values(t.visible))
	slices.SortFunc(histories, func(a, b mekugiHistory) int { return cmp.Compare(a.sequence, b.sequence) })
	local := slices.Collect(maps.Values(t.local))
	slices.SortFunc(local, func(a, b mekugiHistory) int { return cmp.Compare(a.sequence, b.sequence) })
	histories = append(histories, local...)
	var aliases []mekugi.TargetAlias
	for _, history := range histories {
		if history.root == t.directory && (history.confirmed || history.applied) {
			aliases = append(aliases, history.aliases...)
		}
	}
	return aliases
}
