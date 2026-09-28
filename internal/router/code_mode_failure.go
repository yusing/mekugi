package router

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
)

// codeModeFailureChars bounds the script error shown for a failed cell.
const codeModeFailureChars = 240

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
	}
}

// codeModeFailureText reads the host's failure header and, when the host
// appended one as the final part, the first line of its script error.
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
			for line := range strings.SplitSeq(detail, "\n") {
				if line = strings.TrimSpace(livediff.Safe(line, false)); line != "" {
					text += ": " + clipExploreText(line, codeModeFailureChars)
					break
				}
			}
		}
	}
	return text, true
}
