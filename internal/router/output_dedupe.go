package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/outputdedupe"
)

// This is the last request projection. All evidence consumers must have already
// read the original host output. Its index belongs only to this input view.
func projectDuplicateOutputs(request *parsedResponsesRequest, hostParts map[string][]int, execName string) {
	var items []jsonv1.RawMessage
	if json.Unmarshal(request.fields["input"], &items) != nil {
		return
	}
	index := outputdedupe.New()
	defer index.Close()
	commands := make(map[string]string)
	var labels []string
	changed := false
	projectBody := func(header, body, label string) string {
		if len(body) < outputdedupe.Threshold {
			return header + body
		}
		projection := index.Project(body)
		source := len(labels)
		labels = append(labels, label)
		index.Add(source, projection)
		if len(projection.Spans) == 0 {
			return header + body
		}
		var result strings.Builder
		result.WriteString(header)
		start := 0
		for _, span := range projection.Spans {
			result.WriteString(body[start:span.Start])
			fmt.Fprintf(&result, "[same as `%s`", labels[span.Source])
			if !span.Whole {
				if span.FirstLine == span.LastLine {
					fmt.Fprintf(&result, " L%d", span.FirstLine)
				} else {
					fmt.Fprintf(&result, " L%d-%d", span.FirstLine, span.LastLine)
				}
			}
			result.WriteByte(']')
			if body[span.End-1] == '\n' {
				result.WriteByte('\n')
			}
			start = span.End
		}
		result.WriteString(body[start:])
		return result.String()
	}
	projectFrame := func(text string) string {
		header, body, ok := duplicateAttachmentBody(text)
		if !ok {
			return text
		}
		return projectBody(header, body, duplicateOutputLabel(header, ""))
	}
	projectAttachment := func(text string) string {
		frames, ok := decodeFileAttachments(text)
		if !ok {
			return text
		}
		changed := false
		for i, frame := range frames {
			frames[i] = projectFrame(frame)
			changed = changed || frames[i] != frame
		}
		if changed {
			return encodeFileAttachments(frames)
		}
		return text
	}
	for at, raw := range items {
		var item map[string]jsonv1.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		id := jsonString(item, "call_id")
		if jsonString(item, "type") == "custom_tool_call" {
			commands[id] = duplicateOutputCommand(item, execName)
		}
		if jsonString(item, "role") == "user" {
			var metadata struct {
				Kinds []string `json:"content_item_kinds"`
			}
			_ = json.Unmarshal(item["internal_chat_message_metadata_passthrough"], &metadata)
			if content, ok := projectDuplicateTextParts(item["content"], -1, true, metadata.Kinds, projectAttachment); ok {
				item["content"] = content
				items[at] = mustMarshalJSON(item)
				changed = true
			}
			continue
		}
		if !duplicateOutputItem(item) || len(hostParts[id]) == 0 {
			continue
		}
		count := hostParts[id][0]
		hostParts[id] = hostParts[id][1:]
		if count == 0 {
			continue
		}
		projectText := func(text string) string {
			header, body := duplicateOutputBody(text)
			if nativeJSONSession(body) != 0 {
				return text
			}
			return projectBody(header, body, duplicateOutputLabel(commands[id], body))
		}
		if output, ok := projectDuplicateTextParts(item["output"], count, false, nil, projectText); ok {
			item["output"] = output
			items[at] = mustMarshalJSON(item)
			changed = true
		}
	}
	if changed {
		request.setInput(mustMarshalJSON(items))
	}
}

// Project only complete attachment frames, never ordinary prompts or failure
// receipts. Snapshot byte ranges remain those of the original submitted body.
func duplicateAttachmentBody(text string) (header, body string, ok bool) {
	first, body, found := strings.Cut(text, "\n")
	if !found || !strings.Contains(first, " (UTF-8 bytes ") ||
		!strings.HasSuffix(first, attachmentReadGuidance+"):") ||
		!strings.HasPrefix(first, "Attached file ") && !managedSkillFrame(text) {
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
			return mustMarshalJSON(projected), true
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
		kind := jsonString(part, "type")
		if kind != "input_text" || json.Unmarshal(part["text"], &text) != nil {
			continue
		}
		if projected := project(text); projected != text {
			part["text"] = mustMarshalJSON(projected)
			parts[at] = mustMarshalJSON(part)
			changed = true
		}
	}
	if changed {
		return mustMarshalJSON(parts), true
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
		id := jsonString(item, "call_id")
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
	if jsonString(item, "call_id") == "" {
		return false
	}
	switch jsonString(item, "type") {
	case "custom_tool_call_output", "function_call_output":
		return true
	}
	return false
}

func duplicateOutputBody(text string) (header, body string) {
	body = text
	if status, _, payload := codeModeExecutionHeader(text); status != "" {
		body = payload
	} else if state, payload := nativeExecutionHeader(text); state != "" {
		body = payload
	}
	return text[:len(text)-len(body)], body
}

func duplicateOutputCommand(item map[string]jsonv1.RawMessage, execName string) string {
	if jsonString(item, "type") != "custom_tool_call" || execName == "" ||
		strings.TrimPrefix(jsonString(item, "name"), "functions.") != strings.TrimPrefix(execName, "functions.") {
		return ""
	}
	if namespace := jsonString(item, "namespace"); namespace != "" && namespace != "functions" {
		return ""
	}
	if calls := codeModeShellFragments(jsonString(item, "input")); len(calls) == 1 {
		return calls[0].cmd
	}
	return ""
}

func duplicateOutputLabel(command, body string) string {
	if command == "" {
		command, _, _ = strings.Cut(body, "\n")
	}
	// Keep the reference on one line with one inline-code label.
	label := strings.ReplaceAll(strings.Join(strings.Fields(command), " "), "`", "'")
	runes := []rune(label)
	if len(runes) > 60 {
		label = string(runes[:57]) + "..."
	}
	return label
}
