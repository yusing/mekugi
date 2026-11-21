package executionobservation

import (
	"cmp"
	"encoding/json"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	jsonv2 "encoding/json/v2"
	"strings"
)

// observeCodeModeFailures annotates exec cells that failed, such as on a
// JavaScript syntax error. The host emits no item for the cell itself, so a
// script that failed before any nested tool call would otherwise leave no row.
// Only outputs new in this request are read; the result passes unchanged.
func (p *Preparer) observeCodeModeFailures(thread, codeModeToolName string, request *codexresponses.Request, visible map[string]mekugiHistory) {
	if thread == "" || codeModeToolName == "" {
		return
	}
	var items []map[string]json.RawMessage
	if jsonv2.Unmarshal(request.Fields["input"], &items) != nil {
		return
	}
	trailing := len(items)
	for trailing > 0 && strings.HasSuffix(responses.StringField(items[trailing-1], "type"), "_output") {
		trailing--
	}
	calls := make(map[string]map[string]json.RawMessage)
	waitCells := make(map[string]string)
	cellExecCalls := make(map[string]string)
	codeModeCall := func(callID string) (map[string]json.RawMessage, bool) {
		call, found := calls[callID]
		if !found {
			if history, ok := visible[callID]; ok {
				call, found = history.UpstreamItem, history.UpstreamItem != nil
			}
		}
		if !found || responses.StringField(call, "type") != "custom_tool_call" || responses.StringField(call, "name") != codeModeToolName ||
			responses.StringField(call, "namespace") != "" && responses.StringField(call, "namespace") != "functions" {
			return nil, false
		}
		return call, true
	}
	for _, item := range items[:trailing] {
		callID := responses.StringField(item, "call_id")
		switch responses.StringField(item, "type") {
		case "custom_tool_call":
			calls[callID] = item
		case "function_call":
			if strings.TrimPrefix(responses.StringField(item, "name"), "functions.") == "wait" &&
				(responses.StringField(item, "namespace") == "" || responses.StringField(item, "namespace") == "functions") {
				var args struct {
					CellID string `json:"cell_id"`
				}
				if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) == nil && args.CellID != "" {
					waitCells[callID] = args.CellID
				}
			}
		case "custom_tool_call_output":
			if _, ok := codeModeCall(callID); !ok {
				continue
			}
			if texts := binding.ExecutionOutputTexts(item["output"]); len(texts) > 0 {
				if status, cell, _ := binding.CodeModeExecutionHeader(texts[0]); cell != "" && (status == "running" || status == "Script failed") {
					cellExecCalls[cell] = callID
				}
			}
		case "function_call_output":
			if cellID := waitCells[callID]; cellID != "" {
				if texts := binding.ExecutionOutputTexts(item["output"]); len(texts) > 0 {
					if status, nextCell, _ := binding.CodeModeExecutionHeader(texts[0]); nextCell == "" || status != "running" && status != "Script failed" {
						delete(cellExecCalls, cellID)
					}
				}
			}
		}
	}
	for _, item := range items[trailing:] {
		callID := responses.StringField(item, "call_id")
		if callID == "" {
			continue
		}
		switch responses.StringField(item, "type") {
		case "custom_tool_call_output":
			call, ok := codeModeCall(callID)
			if !ok {
				continue
			}
			texts := binding.ExecutionOutputTexts(item["output"])
			if text, failed := binding.CodeModeFailureText(texts); failed {
				_, cell, _ := binding.CodeModeExecutionHeader(texts[0])
				// Wait for the final paged wait output when Script error: has not arrived yet.
				if cell == "" || text != "exec script failed" {
					p.Audience.Collect(activitypresentation.Supplement{Thread: thread, Source: "code-mode-failure\x00" + callID, Kind: "error", CallID: callID, Text: text})
				}
			}
			if codeModeReturnedNothing(item["output"]) {
				source := responses.StringField(call, "input")
				if history, ok := visible[callID]; ok {
					// The host ran the translated carrier, not the model's script.
					source = cmp.Or(history.CarrierPayload, history.Script)
				}
				p.Audience.NoteUnreturned(thread, p.NativeTrace.ReadCell(thread, callID, source).ExecCommands())
			}
		case "function_call_output":
			cellID := waitCells[callID]
			execCallID := cellExecCalls[cellID]
			if cellID == "" || execCallID == "" {
				continue
			}
			texts := binding.ExecutionOutputTexts(item["output"])
			if text, failed := binding.CodeModeFailureText(texts); failed {
				_, nextCell, _ := binding.CodeModeExecutionHeader(texts[0])
				if nextCell == "" || text != "exec script failed" {
					p.Audience.Collect(activitypresentation.Supplement{Thread: thread, Source: "code-mode-failure\x00" + execCallID, Kind: "error", CallID: execCallID, Text: text})
				}
			}
		}
	}
}

// codeModeReturnedNothing reports a finished cell whose result carries nothing
// beyond the host's header and, for a failure, its script error: the model saw
// none of its nested tools' results. A yielded cell's result arrives later.
func codeModeReturnedNothing(raw json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parts = append(parts, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"input_text", text})
	} else if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	header := parts[0].Text
	if strings.HasSuffix(header, "\nOutput:") {
		header += "\n"
	}
	status, cell, body := binding.CodeModeExecutionHeader(header)
	if parts[0].Type != "input_text" || cell != "" || status != "Script completed" && status != "Script failed" || strings.TrimSpace(body) != "" {
		return false
	}
	for i, part := range parts[1:] {
		switch {
		case part.Type != "input_text":
			return false
		case strings.TrimSpace(part.Text) == "",
			status == "Script failed" && i == len(parts)-2 && strings.HasPrefix(part.Text, "Script error:\n"):
		default:
			return false
		}
	}
	return true
}
