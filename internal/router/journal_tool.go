package router

import (
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

type journalListItem struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Question string `json:"question,omitempty"`
	Author   string `json:"author"`
	Reported bool   `json:"reported"`
	Flushed  bool   `json:"flushed"`
}

func journalMutationsSchema() json.RawMessage {
	return mustMarshalJSON(map[string]any{
		"type": "array", "maxItems": maxJournalItems,
		"description": "Optional atomic journal mutations applied before this operation. report_now shows progress immediately.",
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"op":         map[string]any{"type": "string", "enum": []string{"add", "edit", "delete"}},
				"id":         map[string]any{"type": "string"},
				"answer":     map[string]any{"type": "boolean", "description": "Mark text as an answer to the latest user message or native assignment to this child; edit preserves the association when omitted and clears it when false."},
				"text":       map[string]any{"type": "string"},
				"report_now": map[string]any{"type": "boolean"},
			}, "required": []string{"op"},
		},
	})
}

func exposeJournalTool(fields map[string]json.RawMessage, catalog *responsesToolCatalog) error {
	var check func(*responsesToolSection) error
	check = func(section *responsesToolSection) error {
		if section.err != nil {
			return section.err
		}
		for _, tool := range section.tools {
			if tool == nil || tool.fields == nil {
				continue
			}
			if tool.Name == journalToolName || tool.Name == "functions.journal" {
				return errors.New("request already defines journal")
			}
			if tool.nested != nil {
				if err := check(tool.nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(catalog.top); err != nil {
		return err
	}
	for _, group := range catalog.additional {
		if err := check(group.tools); err != nil {
			return err
		}
	}
	catalog.appendTop([]*responsesToolDefinition{newResponsesToolDefinition(map[string]json.RawMessage{
		"type":        mustMarshalJSON("function"),
		"name":        mustMarshalJSON(journalToolName),
		"description": mustMarshalJSON("Manage the calling thread's durable milestone journal. Mutations return router-assigned IDs. Finish ends the turn with its final journal entries, without another model request."),
		"strict":      mustMarshalJSON(false),
		"parameters": mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op":         map[string]any{"type": "string", "enum": []string{"list", "add", "edit", "delete", "finish"}},
				"id":         map[string]any{"type": "string", "description": "Router-assigned item ID; required for edit and delete."},
				"text":       map[string]any{"type": "string", "description": "Required nonblank milestone text for add and edit."},
				"answer":     map[string]any{"type": "boolean", "description": "For add/edit, associate text with the latest user message or native assignment to this child. Omit on edit to preserve; false clears it."},
				"journal":    journalMutationsSchema(),
				"agent":      map[string]any{"type": "string", "description": "Canonical path of a proven ancestor or descendant, for list only. Defaults to the caller."},
				"report_now": map[string]any{"type": "boolean"},
			},
			"required": []string{"op"},
		}),
	})})
	return catalog.encodeTop(fields)
}

func isJournalCall(item map[string]json.RawMessage) bool {
	return jsonString(item, "type") == "function_call" && jsonString(item, "name") == journalToolName &&
		(jsonString(item, "namespace") == "" || jsonString(item, "namespace") == "functions")
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
		if jsonString(prior, "arguments") != jsonString(item, "arguments") {
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
		Agent   string          `json:"agent"`
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
			} else {
				result = map[string]any{"ok": true, "finish_requested": true}
			}
		} else if args.Op == "list" {
			if args.ID != "" || args.Text != nil || args.Answer != nil || args.ReportNow {
				err = errors.New("journal list accepts only agent and batched journal mutations")
			}
			var items []journalItem
			if err == nil {
				if args.Agent != "" {
					items, err = t.proxy.journals.listAgent(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, args.Agent)
				} else {
					items, err = t.proxy.journals.list(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID)
				}
			}
			listed := make([]journalListItem, 0, len(items))
			for _, item := range items {
				listed = append(listed, journalListItem{ID: item.ID, Text: item.Text, Question: item.Question, Author: item.Author, Reported: item.Reported, Flushed: item.Flushed})
			}
			result = map[string]any{"ok": true, "items": listed}
		} else if args.Agent != "" {
			err = errors.New("agent is only supported by journal list")
		} else {
			var ids []string
			ids, err = t.proxy.journals.apply(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, callID, bindJournalAnswers([]journalMutation{args.journalMutation}, t.journalQuestion))
			if err == nil {
				t.featureTrace.record("journal", "tool", "mutation", "accepted", callID, "")
				result = map[string]any{"ok": true, "id": ids[0]}
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
		if history.ToolName == journalHistoryTool {
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
			if history, ok := visible[callID]; ok && history.ToolName == journalHistoryTool {
				if !isJournalCall(history.UpstreamItem) {
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
func journalClientResult(result map[string]json.RawMessage) map[string]json.RawMessage {
	item := maps.Clone(result)
	item["id"] = mustMarshalJSON(journalClientResultID(jsonString(result, "call_id")))
	item["name"] = mustMarshalJSON(journalToolName)
	item["namespace"] = mustMarshalJSON("functions")
	delete(item, "call_id")
	return item
}

func journalResultEvent(result map[string]json.RawMessage) []byte {
	return mustMarshalJSON(map[string]any{"type": responseevents.OutputItemDone, "item": journalClientResult(result)})
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
		if isJournalCall(event.Item) {
			id := jsonString(event.Item, "id")
			if id == "" {
				return nil, true, errors.New("journal call has no item ID")
			}
			t.journalPending[id] = true
			return nil, true, nil
		}
	case event.Type.FunctionArguments():
		if t.journalPending[event.ItemID] {
			return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, true, nil
		}
	case event.Type == responseevents.OutputItemDone:
		t.journalProviderOutput = append(t.journalProviderOutput, event.Item)
		if isJournalCall(event.Item) {
			delete(t.journalPending, jsonString(event.Item, "id"))
			if jsonString(event.Item, "status") == "incomplete" {
				return nil, true, nil
			}
			result, err := t.executeJournalCall(event.Item)
			if err != nil {
				return nil, true, err
			}
			return [][]byte{journalResultEvent(result)}, true, nil
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
				// Some providers complete calls only in the terminal snapshot.
				// Apply those calls before deciding whether to continue or flush,
				// and emit the same client result as an item-done event would.
				if isJournalCall(item) {
					if jsonString(item, "status") == "incomplete" || jsonString(item, "status") == "in_progress" {
						return nil, true, errors.New("incomplete journal call at completion")
					}
					delete(t.journalPending, jsonString(item, "id"))
					seen := t.journalCalls[jsonString(item, "call_id")] != nil
					result, err := t.executeJournalCall(item)
					if err != nil {
						return nil, true, err
					}
					if !seen {
						results = append(results, journalResultEvent(result))
					}
				}
				if !isJournalCall(item) && blocksTokenUsage(item) {
					t.journalClientCalls = true
				}
			}
		}
		if len(t.pending) != 0 {
			return nil, true, staticCriticalDiagnostic("terminal_incomplete_mekugi_call", "the upstream completed with an incomplete HPATCH call")
		}
		t.journalTerminal = t.journalTerminalReady()
		if !t.journalClientCalls && !t.journalTerminal {
			if len(t.journalPending) != 0 {
				return nil, true, errors.New("incomplete journal call at completion")
			}
			// The intermediate terminal is local, but its provider output is not.
			// Complete snapshot-only items for streaming clients, then retain the
			// normal client projection for the eventual terminal and replay.
			for _, item := range t.journalProviderOutput {
				if isJournalCall(item) || slices.ContainsFunc(streamed, func(prior map[string]json.RawMessage) bool {
					if id := jsonString(item, "id"); id != "" {
						return jsonString(prior, "id") == id
					}
					return string(mustMarshalJSON(prior)) == string(mustMarshalJSON(item))
				}) {
					continue
				}
				visible, err := t.transformNonJournalSSE(mustMarshalJSON(map[string]any{
					"type": responseevents.OutputItemDone, "item": item,
				}))
				if err != nil {
					return nil, true, err
				}
				results = append(results, visible...)
			}
			if _, _, err := t.transformResponse(mustMarshalJSON(event.Response), "completed"); err != nil {
				return nil, true, err
			}
			t.journalContinue = true
			return append(t.finalAnswer.flush(), results...), true, nil
		}
		return results, false, nil
	}
	return nil, false, nil
}
