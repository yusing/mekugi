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
	for _, item := range items[:trailing] {
		if responses.StringField(item, "type") == "custom_tool_call" {
			calls[responses.StringField(item, "call_id")] = item
		}
	}
	for _, item := range items[trailing:] {
		callID := responses.StringField(item, "call_id")
		if callID == "" || responses.StringField(item, "type") != "custom_tool_call_output" {
			continue
		}
		call, found := calls[callID]
		if !found {
			if history, ok := visible[callID]; ok {
				call, found = history.UpstreamItem, history.UpstreamItem != nil
			}
		}
		if !found || responses.StringField(call, "type") != "custom_tool_call" || responses.StringField(call, "name") != codeModeToolName ||
			responses.StringField(call, "namespace") != "" && responses.StringField(call, "namespace") != "functions" {
			continue
		}
		if text, failed := binding.CodeModeFailureText(binding.ExecutionOutputTexts(item["output"])); failed {
			p.activity.Collect(activitypresentation.Supplement{Thread: thread, Source: "code-mode-failure\x00" + callID, Kind: "error", CallID: callID, Text: text})
		}
		if codeModeReturnedNothing(item["output"]) {
			source := responses.StringField(call, "input")
			if history, ok := visible[callID]; ok {
				// The host ran the translated carrier, not the model's script.
				source = cmp.Or(history.CarrierPayload, history.Script)
			}
			p.activity.NoteUnreturned(thread, p.nativeTrace.ReadCell(thread, callID, source).ExecCommands())
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
	status, _, body := binding.CodeModeExecutionHeader(header)
	if parts[0].Type != "input_text" || status != "Script completed" && status != "Script failed" || strings.TrimSpace(body) != "" {
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
