package router

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"strings"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

const journalToolName = "journal"
const journalHistoryTool = "__mekugi_journal"

const codeModeJournalStart = "<!-- mekugi-journal:start -->"
const codeModeJournalEnd = "<!-- mekugi-journal:end -->"

const codeModeJournalHint = "In Code Mode, use the exec-local journal helper for reads and mutations: record mutations with await journal(...) inside your next useful exec call, and finish naturally with an answer instead of a journal call."

var journalToolDescription = embeddedInstruction("journal_tool")

//go:embed journal_input.d.ts
var journalInputTypes string

var codeModeJournalGuidance = strings.NewReplacer(
	"<journal-tool-description />", journalToolDescription,
	"<journal-input-types />", strings.TrimSpace(journalInputTypes),
).Replace(embeddedInstruction("journal_code_mode"))

type journalListItem struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Question string `json:"question,omitempty"`
	Author   string `json:"author"`
	Reported bool   `json:"reported"`
	Flushed  bool   `json:"flushed"`
}

func journalMutationsSchema() json.RawMessage {
	text := map[string]any{"type": "string"}
	state := map[string]any{"type": "string", "enum": []string{"pending", "working", "done", "blocked", "dropped"}}
	agent := map[string]any{"type": "string", "description": "Bind a direct child journal on task add or set; the mount is read-only and the binding immutable."}
	// This schema is projected under properties.journal in the host tool's
	// parameters. Local references resolve against that complete input schema.
	tasks := map[string]any{"type": "array", "maxItems": maxJournalItems, "items": map[string]any{
		"anyOf": []any{text, map[string]any{"$ref": "#/properties/journal/$defs/task"}},
	}}
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	op := func(name string) map[string]any {
		return map[string]any{"type": "string", "enum": []string{name}}
	}
	return mustMarshalJSON(map[string]any{
		"type": "array", "maxItems": maxJournalItems,
		"description": embeddedInstruction("journal_mutations"),
		"$defs": map[string]any{"task": object(map[string]any{
			"p": text, "title": text, "body": text, "state": state, "reason": text, "tasks": tasks,
		}, "title")},
		"items": map[string]any{"anyOf": []any{
			object(map[string]any{
				"op": op("plan"), "under": text, "tasks": tasks,
				"reset": map[string]any{"type": "string", "enum": []string{"slice"}},
			}, "op", "tasks"),
			object(map[string]any{
				"op": op("add"), "under": text, "title": text, "body": text, "before": text,
				"kind":  map[string]any{"type": "string", "enum": []string{"task"}},
				"state": state, "reason": text, "agent": agent,
			}, "op", "kind", "title"),
			object(map[string]any{
				"op": op("add"), "under": text, "title": text, "body": text, "before": text,
				"kind": map[string]any{"type": "string", "enum": []string{"note", "context"}},
			}, "op", "title"),
			object(map[string]any{
				"op": op("set"), "p": text, "title": text, "body": text, "state": state, "reason": text, "agent": agent,
			}, "op", "p"),
			object(map[string]any{"op": op("log"), "p": text, "text": text}, "op", "text"),
			object(map[string]any{"op": op("remove"), "p": text}, "op", "p"),
		}},
	})
}

func injectCodeModeJournalGuidance(description string) (string, error) {
	return refreshMarkedToolGuidance(description, codeModeJournalStart, codeModeJournalEnd, codeModeJournalGuidance)
}

func isJournalCall(item map[string]json.RawMessage) bool {
	return jsonString(item, "type") == "function_call" && jsonString(item, "name") == journalToolName &&
		(jsonString(item, "namespace") == "" || jsonString(item, "namespace") == "functions")
}

func isRouterLocalCall(item map[string]json.RawMessage) bool {
	return isJournalCall(item) || isReportIssueCall(item)
}

func routerLocalHistoryTool(item map[string]json.RawMessage) string {
	if isJournalCall(item) {
		return journalHistoryTool
	}
	if isReportIssueCall(item) {
		return reportIssueHistoryTool
	}
	return ""
}

func (t *mekugiResponseTransform) executeRouterLocalCall(item map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if isReportIssueCall(item) {
		return t.executeReportIssueCall(item)
	}
	return t.executeJournalCall(item)
}

func (t *mekugiResponseTransform) executeJournalCall(item map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if t.journalCalls == nil {
		t.journalCalls = make(map[string]map[string]json.RawMessage)
	}
	callID := jsonString(item, "call_id")
	if callID == "" {
		return nil, errors.New("journal call requires a call ID")
	}
	if prior := t.journalCalls[callID]; prior != nil {
		if !isJournalCall(prior) || jsonString(prior, "arguments") != jsonString(item, "arguments") {
			return nil, errors.New("journal call changed arguments")
		}
		for _, result := range t.journalResults {
			if jsonString(result, "call_id") == callID {
				return result, nil
			}
		}
	}
	var args struct {
		journalMutation
		Journal json.RawMessage `json:"journal"`
		Depth   *int            `json:"depth"`
		View    string          `json:"view"`
	}
	decoder := json.NewDecoder(strings.NewReader(jsonString(item, "arguments")))
	decoder.DisallowUnknownFields()
	var batchedIDs []string
	var result any
	if err := decoder.Decode(&args); err != nil || decoder.Decode(new(any)) != io.EOF {
		result = map[string]any{"ok": false, "error": "invalid journal arguments"}
	} else if args.Op == "finish" && (args.ID != "" || args.Text != nil || args.Answer != nil || args.Agent != "" || args.ReportNow) {
		result = map[string]any{"ok": false, "error": "journal finish accepts only op and batched journal mutations"}
	} else {
		var err error
		if len(args.Journal) != 0 {
			var mutations []journalMutation
			mutations, err = decodeJournalMutations(args.Journal)
			if err == nil {
				batchedIDs, err = t.proxy.journals.apply(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, callID+":journal", bindJournalAnswers(mutations, t.journalQuestion))
			}
		}
		if err != nil {
			// Batched model mistakes are correctable tool results, like primary mutations.
		} else if args.Op == "finish" {
			if !t.journalAvailable {
				err = errors.New("journal terminal delivery unavailable")
			} else if t.journalClientCalls {
				err = errors.New(journalFinishHostCallsError)
			} else {
				result = map[string]any{"ok": true, "finish_requested": true}
			}
		} else if args.Op == "list" || args.Op == "read" {
			if args.ID != "" || args.Text != nil || args.Answer != nil || args.ReportNow || args.Under != "" || args.Kind != "" || args.Title != nil || args.Body != nil || args.State != nil || args.Reason != nil || args.Tasks != nil || args.Reset != "" || args.Before != "" || args.Op == "list" && (args.P != "" || args.Depth != nil || args.View != "") {
				err = fmt.Errorf("journal %s accepts only %s and batched journal mutations", args.Op, map[string]string{"list": "agent", "read": "p, agent, depth, view"}[args.Op])
			}
			var items []journalItem
			if err == nil && args.Op == "list" {
				if args.Agent != "" {
					items, err = t.proxy.journals.listAgent(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, args.Agent)
				} else {
					items, err = t.proxy.journals.list(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID)
				}
			}
			if args.Op == "read" {
				var nodes []journalNode
				if err == nil {
					nodes, err = t.proxy.journals.readTree(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, args.Agent, args.P, args.Depth, args.View)
				}
				result = map[string]any{"ok": true, "items": nodes}
			} else {
				listed := make([]journalListItem, 0, len(items))
				for _, item := range items {
					listed = append(listed, journalListItem{ID: item.ID, Text: item.Text, Question: item.Question, Author: item.Author, Reported: item.Reported, Flushed: item.Flushed})
				}
				result = map[string]any{"ok": true, "items": listed}
			}
			if err == nil {
				t.proxy.countJournalRead(t.ctx, t.directory, t.shellThreadID, "counter-read:"+callID, args.Op)
			}
		} else if args.View != "" || args.Depth != nil {
			err = errors.New("journal view and depth require read")
		} else {
			var ids []string
			ids, err = t.proxy.journals.apply(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, callID, bindJournalAnswers([]journalMutation{args.journalMutation}, t.journalQuestion))
			if err == nil {
				t.featureTrace.record("journal", "tool", "mutation", "accepted", callID, "")
				result = map[string]any{"ok": true}
				if args.Op == "plan" || args.Title != nil || args.P != "" {
					result.(map[string]any)["items"] = ids
				}
				if len(ids) != 0 {
					result.(map[string]any)["id"] = ids[0]
				}
			}
		}
		if err != nil {
			result = map[string]any{"ok": false, "error": err.Error()}
		}
	}
	if len(batchedIDs) != 0 {
		t.featureTrace.record("journal", "tool_field", "mutation", "accepted", callID, "")
		result.(map[string]any)["journal_ids"] = batchedIDs
	}
	if t.codeModeToolName != "" && (args.Op != "list" || len(args.Journal) != 0) {
		// Off-schema mutations still apply: rejecting them would add a correction
		// request, even when the same response already carries the final answer.
		result.(map[string]any)["hint"] = codeModeJournalHint
	}
	output := map[string]json.RawMessage{
		"type":    mustMarshalJSON("function_call_output"),
		"call_id": mustMarshalJSON(callID),
		"output":  mustMarshalJSON(string(mustMarshalJSON(result))),
	}
	t.recordLocal(callID, &mekugiHistory{
		ToolName: journalHistoryTool, Script: jsonString(item, "arguments"),
		CarrierKind: codeModeCarrierFunction, CarrierName: journalToolName,
		CarrierPayload: jsonString(item, "arguments"), UpstreamItem: item,
	})
	if err := t.commitLocalCall(callID); err != nil {
		return nil, err
	}
	t.journalCalls[callID] = item
	t.journalResults = append(t.journalResults, output)
	if args.Op == "finish" && result.(map[string]any)["ok"] == true {
		t.journalFinishRequested = true
	}
	return output, nil
}

// Completion is invocation-local. Replaying a retained finish result must never
// finish a later turn, and client-dispatched work still belongs to the host.
func (t *mekugiResponseTransform) journalTerminalReady() bool {
	if !t.journalAvailable || !t.journalFinishRequested {
		return false
	}
	if t.journalClientCalls || len(t.journalPending) != 0 {
		return false
	}
	for _, result := range t.journalResults {
		if call := t.journalCalls[jsonString(result, "call_id")]; isReportIssueCall(call) {
			continue
		}
		var outcome struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal([]byte(jsonString(result, "output")), &outcome) != nil || !outcome.OK {
			return false
		}
	}
	return true
}

// The client retains local results but never dispatches these router calls.
// Replay supplies the exact call alongside its result, not an invented host tool.
func restoreJournalCalls(request *parsedResponsesRequest, visible map[string]mekugiHistory) error {
	var input []map[string]json.RawMessage
	if json.Unmarshal(request.fields["input"], &input) != nil {
		return nil
	}
	seen := make(map[string]bool)
	results := make(map[string]string)
	for callID, history := range visible {
		if history.ToolName == journalHistoryTool || history.ToolName == reportIssueHistoryTool {
			results[journalClientResultID(callID)] = callID
		}
	}
	var restored []map[string]json.RawMessage
	changed := false
	rebase := false
	for _, item := range input {
		// A named local result was never submitted to the provider. Even when
		// this request has no matching workspace record, its cached parent can
		// still contain the intercepted call. Replay the visible standalone
		// result without that parent, rather than borrowing another workspace
		// or leaving the provider's call unanswered.
		rebase = rebase || journalResultCallID(item) != ""
		callID := jsonString(item, "call_id")
		if jsonString(item, "type") == "function_call" {
			seen[callID] = true
		}
		if jsonString(item, "type") == "function_call_output" {
			if callID == "" {
				callID = results[jsonString(item, "id")]
			}
			if history, ok := visible[callID]; ok && history.ToolName == routerLocalHistoryTool(history.UpstreamItem) && history.ToolName != "" {
				if !isRouterLocalCall(history.UpstreamItem) {
					return fmt.Errorf("invalid journal replay call %q", callID)
				}
				if !seen[callID] {
					restored = append(restored, history.UpstreamItem)
					seen[callID] = true
				}
				item = maps.Clone(item)
				delete(item, "id")
				delete(item, "name")
				delete(item, "namespace")
				item["call_id"] = mustMarshalJSON(callID)
				changed = true
			}
		}
		restored = append(restored, item)
	}
	if changed {
		request.setInput(mustMarshalJSON(restored))
	}
	if changed || rebase {
		// The native boundary no longer describes provider history. This is the
		// safe fallback for callers without confirmed transport evidence; the
		// WebSocket reconciler independently compares the finished projection.
		request.cachedInput = 0
		request.rebaseInput = true
	}
	return nil
}

func journalClientResultID(callID string) string {
	return "fco_mekugi_journal_" + base64.RawURLEncoding.EncodeToString([]byte(callID))
}

func journalResultCallID(item map[string]json.RawMessage) string {
	if jsonString(item, "type") != "function_call_output" || jsonString(item, "call_id") != "" {
		return ""
	}
	encoded, ok := strings.CutPrefix(jsonString(item, "id"), "fco_mekugi_journal_")
	if !ok {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || journalClientResultID(string(decoded)) != jsonString(item, "id") {
		return ""
	}
	return string(decoded)
}

// Codex preserves named, unpaired outputs only when call_id is absent.
// Keep the paired form internally for provider continuation and durable replay.
func journalClientResult(result map[string]json.RawMessage, toolName ...string) map[string]json.RawMessage {
	item := maps.Clone(result)
	item["id"] = mustMarshalJSON(journalClientResultID(jsonString(result, "call_id")))
	name := journalToolName
	if len(toolName) != 0 {
		name = toolName[0]
	}
	item["name"] = mustMarshalJSON(name)
	item["namespace"] = mustMarshalJSON("functions")
	delete(item, "call_id")
	return item
}

func journalResultEvent(result map[string]json.RawMessage, toolName ...string) []byte {
	return mustMarshalJSON(map[string]any{"type": responseevents.OutputItemDone, "item": journalClientResult(result, toolName...)})
}

func (t *mekugiResponseTransform) journalOutputItems() []map[string]json.RawMessage {
	output := slices.Clone(t.journalProviderOutput)
	return append(output, t.journalResults...)
}

// ReleaseDelivery is also called when a transform or downstream write fails.
func (t *mekugiResponseTransform) ReleaseDelivery() {
	if t.journalDeliveryRelease != nil {
		t.journalDeliveryRelease()
		t.journalDeliveryRelease = nil
	}
}

func (t *mekugiResponseTransform) interceptJournalSSE(payload []byte) ([][]byte, bool, error) {
	var event struct {
		Type     responseevents.Kind        `json:"type"`
		ItemID   string                     `json:"item_id"`
		Item     map[string]json.RawMessage `json:"item"`
		Response map[string]json.RawMessage `json:"response"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return nil, false, nil
	}
	switch {
	case event.Type == responseevents.OutputItemAdded:
		if isRouterLocalCall(event.Item) {
			id := jsonString(event.Item, "id")
			if id == "" {
				return nil, true, errors.New("journal call has no item ID")
			}
			t.journalPending[id] = true
			return nil, true, nil
		}
		if blocksTokenUsage(event.Item) {
			t.journalClientCalls = true
		}
	case event.Type.FunctionArguments():
		if t.journalPending[event.ItemID] {
			return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, true, nil
		}
	case event.Type == responseevents.OutputItemDone:
		t.journalProviderOutput = append(t.journalProviderOutput, event.Item)
		if isRouterLocalCall(event.Item) {
			delete(t.journalPending, jsonString(event.Item, "id"))
			if jsonString(event.Item, "status") == "incomplete" {
				return nil, true, nil
			}
			result, err := t.executeRouterLocalCall(event.Item)
			if err != nil {
				return nil, true, err
			}
			if t.deferJournalFinishResult(result) {
				return nil, true, nil
			}
			return [][]byte{journalResultEvent(result, jsonString(event.Item, "name"))}, true, nil
		}
		if blocksTokenUsage(event.Item) {
			t.journalClientCalls = true
		}
	case event.Type == responseevents.Completed:
		var output []map[string]json.RawMessage
		streamed := t.journalProviderOutput
		var results [][]byte
		if json.Unmarshal(event.Response["output"], &output) == nil && len(output) != 0 {
			t.journalProviderOutput = output
			for _, item := range output {
				if !isRouterLocalCall(item) && blocksTokenUsage(item) {
					t.journalClientCalls = true
				}
			}
			for _, item := range output {
				// Some providers complete calls only in the terminal snapshot.
				// Apply those calls before deciding whether to continue or flush,
				// and emit the same client result as an item-done event would.
				if isRouterLocalCall(item) {
					if jsonString(item, "status") == "incomplete" || jsonString(item, "status") == "in_progress" {
						return nil, true, errors.New("incomplete journal call at completion")
					}
					delete(t.journalPending, jsonString(item, "id"))
					seen := t.journalCalls[jsonString(item, "call_id")] != nil
					result, err := t.executeRouterLocalCall(item)
					if err != nil {
						return nil, true, err
					}
					if !seen && !t.deferJournalFinishResult(result) {
						results = append(results, journalResultEvent(result, jsonString(item, "name")))
					}
				}
			}
		}
		results = append(results, t.finishDeferredJournalResults()...)
		naturalResponse := maps.Clone(event.Response)
		if naturalResponse == nil {
			naturalResponse = make(map[string]json.RawMessage)
		}
		if jsonString(naturalResponse, "status") == "" {
			naturalResponse["status"] = mustMarshalJSON("completed")
		}
		if err := t.captureNaturalJournalAnswer(mustMarshalJSON(naturalResponse)); err != nil {
			return nil, true, err
		}
		t.journalTerminal = t.journalTerminalReady()
		if len(t.journalResults) != 0 && !t.journalClientCalls && !t.journalTerminal && !t.journalNaturalFinalSeen {
			if len(t.journalPending) != 0 {
				return nil, true, errors.New("incomplete journal call at completion")
			}
			// The intermediate terminal is local, but its provider output is not.
			// Complete snapshot-only items for streaming clients, then retain the
			// normal client projection for the eventual terminal and replay.
			for _, item := range t.journalProviderOutput {
				if isRouterLocalCall(item) || slices.ContainsFunc(streamed, func(prior map[string]json.RawMessage) bool {
					if id := jsonString(item, "id"); id != "" {
						return jsonString(prior, "id") == id
					}
					return string(mustMarshalJSON(prior)) == string(mustMarshalJSON(item))
				}) {
					continue
				}
				visible, err := t.transformActivitySSE(mustMarshalJSON(map[string]any{
					"type": responseevents.OutputItemDone, "item": item,
				}))
				if err != nil {
					return nil, true, err
				}
				results = append(results, visible...)
			}
			if _, err := t.transformResponse(mustMarshalJSON(event.Response), "completed"); err != nil {
				return nil, true, err
			}
			t.journalContinue = true
			return append(t.finalAnswer.flush(), results...), true, nil
		}
		return results, false, nil
	case event.Type == responseevents.Failed || event.Type == responseevents.Incomplete:
		var output []map[string]json.RawMessage
		if json.Unmarshal(event.Response["output"], &output) == nil {
			for _, item := range output {
				if !isRouterLocalCall(item) && blocksTokenUsage(item) {
					t.journalClientCalls = true
				}
			}
		}
		return t.finishDeferredJournalResults(), false, nil
	}
	return nil, false, nil
}
