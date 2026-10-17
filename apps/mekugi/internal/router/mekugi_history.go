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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
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
	Caller           string `json:",omitempty"`
	Source           string `json:",omitempty"`
	ChangeID         string
	ReviewFiles      []diffmodel.ReviewFile
	NativePatches    []changecapture.PatchObservation `json:",omitempty"`
	ResolvedBaseline *resolvedStockBaseline           `json:",omitempty"`
	ExecObservation  *changecapture.Observation       `json:",omitempty"`
	ExecOutcome      *changecapture.Outcome           `json:",omitempty"`
	HostResults      []binding.ToolResult             `json:",omitempty"`
	CommandSegments  *legacycall.Segments             `json:",omitempty"`
	CarrierName      string
	CarrierKind      codeModeCarrierKind
	CarrierPayload   string

	Report string
	// Deferred diagnostics are projected onto the model-visible result, never
	// evaluated in a program that owns the host's output-helper identifier.
	OutputWarning        string
	TranslationError     string
	CorrelationID        string
	Attempt              int
	UpstreamItem         map[string]json.RawMessage
	ReplayCarrier        bool
	CommentaryMessageIDs []string
	AlreadySatisfied     bool

	confirmed  bool
	nativeCell *nativetrace.Cell
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
	texts := binding.ExecutionOutputTexts(raw)
	// exec prepends its metadata as a separate content block before
	// text(report). Native exec_command instead returns one combined block.
	if len(texts) == 2 && h.CarrierName != nativeExecCommandToolName {
		state, _, body := binding.CodeModeExecutionHeader(texts[0])
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
		state, body := binding.NativeExecutionHeader(text)
		return state == "Process exited with code 0" && body == h.Report
	}
	state, _, body := binding.CodeModeExecutionHeader(text)
	return state == "Script completed" && body == h.Report
}

func orderedCalls(histories map[string]mekugiHistory) []history.Call {
	ids := slices.SortedFunc(maps.Keys(histories), func(a, b string) int {
		return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
	})
	calls := make([]history.Call, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, history.Call{ID: id, History: retainedHistory(histories[id])})
	}
	return calls
}

// reconcileVisibleInput constructs recovery ancestry from the validated request,
// never from a routing session's most recent turn. All changes stay local until
// the entire input is valid, including output confirmations.
func (p *mekugiProxy) reconcileVisibleInput(ctx context.Context, request *codexresponses.Request, workspace, sessionID string) (map[string]mekugiHistory, error) {
	releaseSnapshot, err := p.replayStore.lockStorageSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseSnapshot()
	visible := make(map[string]mekugiHistory)
	raw, ok := request.Fields["input"]
	if !ok {
		raw = json.RawMessage(`[]`)
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return visible, nil
	}
	changed := false
	commentaryIDs := p.calls.CommentaryIDs(sessionID)
	filtered := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		if responses.StringField(item, "type") == "message" {
			id := responses.StringField(item, "id")
			_, generated := commentaryIDs[id]
			if p.replayStore != nil {
				var err error
				generated, err = p.replayStore.hasCommentary(ctx, workspace, id)
				if err != nil {
					return nil, err
				}
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
	var completedCalls []execCompletion
	pendingCalls := make(map[string]execCompletion)
	editOutputs := make(map[string]map[string]json.RawMessage)
	continuations := make(map[string]string)
	for index, item := range items {
		itemType := responses.StringField(item, "type")
		if itemType != "custom_tool_call" && itemType != "function_call" && itemType != "custom_tool_call_output" && itemType != "function_call_output" {
			continue
		}
		callID := responses.StringField(item, "call_id")
		if callID == "" {
			return nil, inferencegateway.IncompatibleRequest("unsupported_tool_history", "A retained tool item has no host call identity. Start a new session.")
		}
		if itemType == "function_call" {
			var args struct {
				CellID    string          `json:"cell_id"`
				SessionID json.RawMessage `json:"session_id"`
			}
			switch strings.TrimPrefix(responses.StringField(item, "name"), "functions.") {
			case "wait":
				if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) == nil && args.CellID != "" {
					continuations[callID] = "cell:" + args.CellID
				}
			case "write_stdin":
				if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) == nil && len(args.SessionID) != 0 {
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
					if result := binding.DecodeExecutionResult(resultTool, item["output"]); result.Terminal {
						pending.result = result
						editOutputs[pending.callID] = item
						completedCalls = append(completedCalls, pending)
						delete(pendingCalls, key)
					}
				}
			}
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
				var retained legacycall.History
				retained, known = p.calls.Lookup(sessionID, callID)
				history = legacyHistory(retained)
			}
			if !known {
				continue
			}
			history.sequence = uint64(len(visible) + 1)
			history.confirmed = false
		}
		if strings.HasPrefix(history.ToolName, "__mekugi_") {
			return nil, inferencegateway.IncompatibleRequest("unsupported_tool_history", "This session contains a retired router-local tool call. Start a new session.")
		}
		carrierKind := history.effectiveCarrierKind()
		if itemType == carrierOutputItemType(carrierKind) {
			if len(history.NativePatches) != 0 || history.ExecObservation != nil || history.ResolvedBaseline != nil {
				completed := execCompletion{callID: callID, history: history, result: binding.DecodeExecutionResult(history.ToolName, item["output"])}
				if completed.result.Terminal {
					completedCalls = append(completedCalls, completed)
					editOutputs[callID] = item
				} else if pending := completed.result.ResumeHandle; pending != "" {
					pendingCalls[pending] = completed
					p.execWindows.setSession(callID, pending)
				}
			}
			if history.confirmsReport(item["output"]) {
				history.confirmed = true
			}
			visible[callID] = history
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
				if responses.StringField(history.UpstreamItem, "type") == carrierItemType(codeModeCarrierFunction) {
					upstreamKind = codeModeCarrierFunction
				}
				item["type"] = responses.MustMarshalJSON(carrierOutputItemType(upstreamKind))
				changed = true
			}
			continue
		}
		if itemType != carrierItemType(carrierKind) {
			return nil, fmt.Errorf("replayed call %q changed item type", callID)
		}
		if responses.StringField(item, "name") != history.CarrierName {
			return nil, fmt.Errorf("replayed call %q changed carrier name", callID)
		}
		if validatedCarriers[callID] {
			return nil, fmt.Errorf("replayed call %q appears more than once", callID)
		}
		if responses.StringField(item, carrierPayloadField(carrierKind)) != history.carrierInput() {
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
			item["name"] = responses.MustMarshalJSON(cmp.Or(history.ToolName, applyPatchToolName))
			item["input"] = responses.MustMarshalJSON(history.Script)
		}
		changed = true
	}
	if err := p.replayStore.retainInput(ctx, workspace, raw, visible, releaseSnapshot); err != nil {
		return nil, err
	}
	releaseSnapshot()
	// The native trace supplies per-tool outcomes even when JavaScript discards
	// or catches a nested result. Never infer them from text printed by the cell.
	for i := range completedCalls {
		completed := &completedCalls[i]
		if completed.history.ToolName != applyPatchToolName && completed.history.ToolName != nativeExecCommandToolName {
			source := completed.history.CarrierPayload
			if source == "" {
				source = completed.history.Script
			}
			completed.history.nativeCell = p.nativeTrace.ReadCell(completed.history.ExecutingThread, completed.callID, source)
			resolveStockEdits(&completed.history, workspace)
		}
	}
	execGroups := make(map[string][]execCompletion)
	for _, completed := range completedCalls {
		// A yielded command owns its continuing process, not the completed
		// patches in its exec cell. Freeze those patches below before a
		// later call can edit their files; only command capture waits here.
		if completed.history.nativeCell.Pending() {
			continue
		}
		key := execSiblingKey(completed.history)
		if key == "" {
			key = completed.callID
		}
		execGroups[key] = append(execGroups[key], completed)
	}
	for _, completed := range completedCalls {
		thread := completed.history.ExecutingThread
		if err := p.finalizeNativePatches(ctx, workspace, thread, completed.callID, completed.history, completed.result); err != nil {
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
	if p.replayStore != nil {
		updates, err := p.replayStore.changes.Repair(ctx, p.replayStore.scoped(ctx).changeScope(), workspace, changePublications(visible))
		if err != nil {
			return nil, changeError(err)
		}
		p.replayStore.notifyChanges(updates)
	}
	for _, completed := range completedCalls {
		item := editOutputs[completed.callID]
		if item == nil {
			continue
		}
		notice, err := p.replayStore.agentEditNotice(ctx, workspace, completed.callID, completed.history)
		if err != nil || notice == "" {
			continue // Auxiliary evidence must not interfere with stock results.
		}
		output, projected, err := appendToolOutputWarning(item["output"], notice)
		if err == nil && projected {
			item["output"] = output
			changed = true
		}
	}
	if changed {
		encoded, err := responses.MarshalJSON(items)
		if err != nil {
			return nil, fmt.Errorf("encode replayed Responses input: %w", err)
		}
		request.Fields["input"] = encoded
	}
	return visible, nil
}

func appendToolOutputWarning(raw json.RawMessage, warning string) (json.RawMessage, bool, error) {
	var parts []json.RawMessage
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parts = []json.RawMessage{responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": text})}
	} else if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, false, errors.New("tool output must be text or content parts")
	}
	notice := responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": warning})
	for _, part := range parts {
		if responses.SameJSONValue(part, notice) {
			return raw, false, nil
		}
	}
	return responses.MustMarshalJSON(append(parts, notice)), true, nil
}

func (t *mekugiResponseTransform) recordLocal(callID string, history *mekugiHistory) {
	t.toolTrace.ToolCall(callID, history.ToolName)
	t.localSequence++
	history.sequence = t.localSequence
	t.local[callID] = *history
}

func (t *mekugiResponseTransform) commitHistory() error {
	if t.historyCommitted {
		return nil
	}
	if err := t.proxy.replayStore.putAfterMaintenance(t.ctx, t.directory, t.local); err != nil {
		return requestexecution.Classify(err, "replay_history_commit", "Mekugi could not persist completed response history", true)
	}
	if err := t.proxy.calls.Remember(t.historySessionID, orderedCalls(t.local)); err != nil && t.proxy.replayStore == nil {
		return requestexecution.Classify(err, "replay_history_commit", "Mekugi could not retain completed response history", true)
	}
	t.historyCommitted = true
	return nil
}

func (t *mekugiResponseTransform) commitLocalCall(callID string) error {
	history, exists := t.local[callID]
	if !exists {
		return nil
	}
	if err := t.proxy.replayStore.putAfterMaintenance(t.ctx, t.directory, map[string]mekugiHistory{callID: history}); err != nil {
		return requestexecution.Classify(err, "replay_call_commit", "Mekugi could not persist a completed tool call", true)
	}
	if err := t.proxy.calls.Remember(t.historySessionID, orderedCalls(map[string]mekugiHistory{callID: history})); err != nil && t.proxy.replayStore == nil {
		return requestexecution.Classify(err, "replay_call_commit", "Mekugi could not retain a completed tool call", true)
	}
	return nil
}
