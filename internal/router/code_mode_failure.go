package router

import (
	"cmp"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
)

// observeCodeModeFailures annotates Code Mode cells that failed, such as on a
// JavaScript syntax error. The host emits no item for the cell itself, so a
// script that failed before any nested tool call would otherwise leave no row.
// Only outputs new in this request are read; the result passes unchanged.
func (p *mekugiProxy) observeCodeModeFailures(thread, codeModeToolName string, request *parsedResponsesRequest, visible map[string]mekugiHistory) {
	if thread == "" || codeModeToolName == "" {
		return
	}
	var items []map[string]json.RawMessage
	if jsonv2.Unmarshal(request.fields["input"], &items) != nil {
		return
	}
	trailing := len(items)
	for trailing > 0 && strings.HasSuffix(jsonString(items[trailing-1], "type"), "_output") {
		trailing--
	}
	calls := make(map[string]map[string]json.RawMessage)
	for _, item := range items[:trailing] {
		if jsonString(item, "type") == "custom_tool_call" {
			calls[jsonString(item, "call_id")] = item
		}
	}
	for _, item := range items[trailing:] {
		callID := jsonString(item, "call_id")
		if callID == "" || jsonString(item, "type") != "custom_tool_call_output" {
			continue
		}
		call, found := calls[callID]
		if !found {
			if history, ok := visible[callID]; ok {
				call, found = history.UpstreamItem, history.UpstreamItem != nil
			}
		}
		if !found || jsonString(call, "type") != "custom_tool_call" || jsonString(call, "name") != codeModeToolName ||
			jsonString(call, "namespace") != "" && jsonString(call, "namespace") != "functions" {
			continue
		}
		if text, failed := codeModeFailureText(executionOutputTexts(item["output"])); failed {
			p.activity.collectEvent(activityEvent{thread: thread, source: "code-mode-failure\x00" + callID, kind: "error", callID: callID, text: text})
		}
		if codeModeReturnedNothing(item["output"]) {
			source := jsonString(call, "input")
			if history, ok := visible[callID]; ok {
				// The host ran the translated carrier, not the model's script.
				source = cmp.Or(history.CarrierPayload, history.Script)
			}
			p.activity.noteUnreturned(thread, p.nativeTrace.readCell(thread, callID, source).execCommands())
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
	status, _, body := codeModeExecutionHeader(header)
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

// codeModeFailureText reads the host's failure header and, when the host
// appended one as the final part, its complete script error. Presentation
// bounds the inline preview separately; the host result is never modified.
func codeModeFailureText(texts []string) (string, bool) {
	if len(texts) == 0 {
		return "", false
	}
	if status, _, _ := codeModeExecutionHeader(texts[0]); status != "Script failed" {
		return "", false
	}
	text := "Code Mode script failed"
	if len(texts) > 1 {
		if detail, ok := strings.CutPrefix(texts[len(texts)-1], "Script error:\n"); ok {
			if strings.TrimSpace(detail) != "" {
				text += ": " + detail
			}
		}
	}
	return text, true
}
