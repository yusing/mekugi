// Source: internal/router/output_dedupe.go:1:258@b9a7b3fe.
package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/modules/outputdeduplication/internal/projection"
)

// This is the last request projection. All evidence consumers must have already
// read the original host output. Its index belongs only to this input view.
func apply(service projection.Service, input []byte, hostParts map[string][]int) (result binding.ProjectionResult) {
	summary := &binding.ProjectionReceipt{TokensMeasured: true}
	result = binding.ProjectionResult{Input: input, Receipt: summary}
	var items []jsonv1.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return
	}
	summary.RepeatedCalls = make(map[string]string)
	var repeatedLines, bodyBytes, repeatedBytes int
	pass := service.NewPass()
	defer pass.Close()
	changed := false
	projectBody := func(header, body string) string {
		result := pass.Text(header, body)
		summary.OutputRepetitions += result.Occurrences
		repeatedLines += result.RepeatedLines
		bodyBytes += result.BodyBytes
		repeatedBytes += result.RepeatedBytes
		return result.Text
	}
	projectFrame := func(text string) string {
		header, body, ok := duplicateAttachmentBody(text)
		if !ok {
			return text
		}
		return projectBody(header, body)
	}
	projectAttachment := func(text string) string {
		frames, ok := attachments.Decode(text)
		if !ok {
			return text
		}
		changed := false
		for i, frame := range frames {
			frames[i] = projectFrame(frame)
			changed = changed || frames[i] != frame
		}
		if changed {
			return attachments.Encode(frames)
		}
		return text
	}
	for at, raw := range items {
		var item map[string]jsonv1.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		id := responses.StringField(item, "call_id")
		if responses.StringField(item, "role") == "user" {
			var metadata struct {
				Kinds []string `json:"content_item_kinds"`
			}
			_ = json.Unmarshal(item["internal_chat_message_metadata_passthrough"], &metadata)
			if content, ok := projectDuplicateTextParts(item["content"], -1, true, metadata.Kinds, projectAttachment); ok {
				item["content"] = content
				items[at] = responses.MustMarshalJSON(item)
				changed = true
			}
			continue
		}
		if !duplicateOutputItem(item) || len(hostParts[id]) == 0 {
			continue
		}
		repeatedLines, bodyBytes, repeatedBytes = 0, 0, 0
		count := hostParts[id][0]
		hostParts[id] = hostParts[id][1:]
		if count == 0 {
			continue
		}
		projectText := func(text string) string {
			header, body := duplicateOutputBody(text)
			if binding.NativeJSONSession(body) != 0 {
				return text
			}
			return projectBody(header, body)
		}
		if output, ok := projectDuplicateTextParts(item["output"], count, false, nil, projectText); ok {
			item["output"] = output
			items[at] = responses.MustMarshalJSON(item)
			changed = true
		}
		if repeatedLines > 0 {
			label := fmt.Sprintf("%d lines repeated", repeatedLines)
			if repeatedBytes == bodyBytes {
				label = "repeated"
			}
			summary.RepeatedCalls[id] = label
		}
	}
	if changed {
		result.Input = responses.MustMarshalJSON(items)
		summary.SavedTokens, summary.TokensMeasured = projection.EstimateSavings(input, result.Input)
	}
	return result
}

// Project only complete attachment frames, never ordinary prompts or failure
// receipts. Snapshot byte ranges remain those of the original submitted body.
func duplicateAttachmentBody(text string) (header, body string, ok bool) {
	first, body, found := strings.Cut(text, "\n")
	if !found || !strings.Contains(first, " (UTF-8 bytes ") ||
		!strings.HasSuffix(first, attachments.ReadGuidance+"):") ||
		!strings.HasPrefix(first, "Attached file ") && !attachments.SkillFrame(text) {
		return "", "", false
	}
	return first + "\n", body, true
}

// A negative count selects all user parts. Host counts exclude router appends.
func projectDuplicateTextParts(raw jsonv1.RawMessage, count int, user bool, kinds []string, project func(string) string) (jsonv1.RawMessage, bool) {
	eligible := func(at int) bool {
		return !user || at >= len(kinds) || kinds[at] == "" || strings.HasPrefix(kinds[at], "user.")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if user {
			// The wire owner expands only array input_text envelopes.
			return raw, false
		}
		if projected := project(text); projected != text {
			return responses.MustMarshalJSON(projected), true
		}
		return raw, false
	}
	var parts []jsonv1.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return raw, false
	}
	if count < 0 {
		count = len(parts)
	}
	changed := false
	for at := 0; at < min(count, len(parts)); at++ {
		if !eligible(at) {
			continue
		}
		var part map[string]jsonv1.RawMessage
		if json.Unmarshal(parts[at], &part) != nil {
			continue
		}
		kind := responses.StringField(part, "type")
		if kind != "input_text" || json.Unmarshal(part["text"], &text) != nil {
			continue
		}
		if projected := project(text); projected != text {
			part["text"] = responses.MustMarshalJSON(projected)
			parts[at] = responses.MustMarshalJSON(part)
			changed = true
		}
	}
	if changed {
		return responses.MustMarshalJSON(parts), true
	}
	return raw, false
}

// Capture this boundary before replay and all router-owned output appends.
// A string is one host part, even when a later append converts it to an array.
func duplicateOutputHostParts(raw jsonv1.RawMessage) map[string][]int {
	var items []map[string]jsonv1.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	counts := make(map[string][]int)
	for _, item := range items {
		if !duplicateOutputItem(item) {
			continue
		}
		id := responses.StringField(item, "call_id")
		var text string
		count := 1
		if json.Unmarshal(item["output"], &text) != nil {
			var parts []jsonv1.RawMessage
			if json.Unmarshal(item["output"], &parts) != nil {
				count = 0
			} else {
				count = len(parts)
			}
		}
		counts[id] = append(counts[id], count)
	}
	return counts
}

func duplicateOutputItem(item map[string]jsonv1.RawMessage) bool {
	if responses.StringField(item, "call_id") == "" {
		return false
	}
	switch responses.StringField(item, "type") {
	case "custom_tool_call_output", "function_call_output":
		return true
	}
	return false
}

func duplicateOutputBody(text string) (header, body string) {
	body = text
	if status, _, payload := binding.CodeModeExecutionHeader(text); status != "" {
		body = payload
	} else if state, payload := binding.NativeExecutionHeader(text); state != "" {
		body = payload
	}
	return text[:len(text)-len(body)], body
}
