// Source: internal/router/output_dedupe.go:1:258@b9a7b3fe.
package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"encoding/hex"
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
	var savings projection.Savings
	pass := service.NewPass(&savings)
	defer pass.Close()
	changed := false
	projectBody := func(header, body string, source bool) string {
		var result projection.Result
		if source {
			result = pass.Source(header, body)
		} else {
			result = pass.Text(header, body)
		}
		summary.OutputRepetitions += result.Occurrences
		repeatedLines += result.RepeatedLines
		bodyBytes += result.BodyBytes
		repeatedBytes += result.RepeatedBytes
		return result.Text
	}
	projectAttachment := func(text string) string {
		snapshot, ok := attachments.DecodeSnapshot(text)
		if !ok {
			return text
		}
		type counts struct{ lines, body, repeated int }
		totals := make(map[string]counts)
		changed := false
		for i := 0; i < len(snapshot.Frames); {
			headers, bodies := attachmentSource(snapshot.Frames[i:])
			if len(bodies) == 0 {
				i++
				continue
			}
			for _, result := range pass.Sources(headers, bodies) {
				frame := snapshot.Frames[i]
				selection, _ := attachments.ParseSelection(frame)
				snapshot.Frames[i] = result.Text + selection.Footer
				summary.OutputRepetitions += result.Occurrences
				changed = changed || snapshot.Frames[i] != frame
				if id := snapshot.ReceiptID(frame); id != "" {
					count := totals[id]
					count.lines += result.RepeatedLines
					count.body += result.BodyBytes
					count.repeated += result.RepeatedBytes
					totals[id] = count
				}
				i++
			}
		}
		for id, count := range totals {
			if label := repetitionLabel(count.lines, count.body, count.repeated); label != "" {
				if summary.RepeatedAttachments == nil {
					summary.RepeatedAttachments = make(map[string]string)
				}
				summary.RepeatedAttachments[id] = label
			}
		}
		if changed {
			projected := snapshot.Encode()
			savings.Add(text, projected)
			return projected
		}
		return text
	}
	projectSources := func(text string) string {
		projected := attachments.ProjectSources(text, func(body string) string {
			return projectBody("", body, true)
		})
		savings.Add(text, projected)
		return projected
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
			project := func(text string) string {
				if strings.HasPrefix(text, "<mekugi-file-attachments-") {
					return projectAttachment(text)
				}
				return projectSources(text)
			}
			if content, ok := projectDuplicateTextParts(item["content"], -1, "user", metadata.Kinds, project); ok {
				item["content"] = content
				items[at] = responses.MustMarshalJSON(item)
				changed = true
			}
			continue
		}
		if suppliedMessage(item) {
			if content, ok := projectDuplicateTextParts(item["content"], -1, "assistant", nil, projectSources); ok {
				item["content"] = content
				items[at] = responses.MustMarshalJSON(item)
				changed = true
			}
			continue
		}
		// Native follow-up continuations retain their attributed message in a
		// named result without a tool call ID. Only typed bodies are eligible.
		if id == "" && responses.StringField(item, "type") == "function_call_output" && responses.StringField(item, "namespace") == "threadcontrol" && responses.StringField(item, "name") == "followup_task" {
			if output, ok := projectDuplicateTextParts(item["output"], 1, "", nil, projectSources); ok {
				item["output"] = output
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
			if strings.Contains(body, "<mekugi-source-v") {
				return header + projectSources(body)
			}
			// Retained file/skill envelopes and historical selection envelopes
			// share submitted source identity with explicit subsequent reloads.
			if strings.Contains(body, "<mekugi-file-attachments-") {
				if projected, valid := projectRetainedAttachments(body, projectAttachment); valid {
					return header + projected
				}
			}
			source := false
			if strings.HasPrefix(id, "compact-") {
				if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasSuffix(first, ": exit_code=0") {
					header, body, source = header+first+"\n", rest, true
				}
			}
			projected := projectBody(header, body, source)
			savings.Add(text, projected)
			return projected
		}
		if output, ok := projectDuplicateTextParts(item["output"], count, "", nil, projectText); ok {
			item["output"] = output
			items[at] = responses.MustMarshalJSON(item)
			changed = true
		}
		if label := repetitionLabel(repeatedLines, bodyBytes, repeatedBytes); label != "" {
			summary.RepeatedCalls[id] = label
		}
	}
	if changed {
		result.Input = responses.MustMarshalJSON(items)
		summary.SavedTokens, summary.TokensMeasured = savings.Result()
	}
	return result
}

// Attributed message sources are not ordinary assistant prose. Keep the native
// identity and part provenance checks ahead of provider-only identity removal.
func suppliedMessage(item map[string]jsonv1.RawMessage) bool {
	if responses.StringField(item, "type") != "message" || responses.StringField(item, "role") != "assistant" || responses.StringField(item, "phase") != "commentary" {
		return false
	}
	id, ok := strings.CutPrefix(responses.StringField(item, "id"), "threadcontrol_")
	if !ok || len(id) != 64 {
		return false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return false
	}
	var metadata struct {
		Kinds []string `json:"content_item_kinds"`
	}
	if json.Unmarshal(item["internal_chat_message_metadata_passthrough"], &metadata) != nil || len(metadata.Kinds) != 1 {
		return false
	}
	var content []map[string]jsonv1.RawMessage
	if json.Unmarshal(item["content"], &content) != nil || len(content) != 1 || responses.StringField(content[0], "type") != "output_text" {
		return false
	}
	return metadata.Kinds[0] == "multi_agent.inter_agent_message" || metadata.Kinds[0] == "multi_agent.inter_agent_completion_message"
}

// Only contiguous chunks of the same complete submitted source form one body.
// Omission notices and malformed framing retain the ordinary per-frame path.
func attachmentSource(frames []string) (headers, bodies []string) {
	header, body, ok := duplicateAttachmentBody(frames[0])
	if !ok {
		return nil, nil
	}
	headers, bodies = []string{header}, []string{body}
	// Current selection chunks share their quote header, without byte coordinates.
	if selection, ok := attachments.ParseSelection(frames[0]); ok && !strings.Contains(header, " (UTF-8 bytes ") {
		for _, frame := range frames[1:] {
			nextHeader, nextBody, ok := duplicateAttachmentBody(frame)
			next, selected := attachments.ParseSelection(frame)
			if !ok || !selected || next.Label != selection.Label || next.Source != selection.Source {
				break
			}
			headers, bodies = append(headers, nextHeader), append(bodies, nextBody)
		}
		return headers, bodies
	}
	identity, coordinates, _ := strings.Cut(header, " (UTF-8 bytes ")
	var start, end, total int
	if n, _ := fmt.Sscanf(coordinates, "%d:%d of %d", &start, &end, &total); n != 3 || start != 0 || end != len(body) || end >= total {
		return headers, bodies
	}
	for _, frame := range frames[1:] {
		header, body, ok := duplicateAttachmentBody(frame)
		key, coordinates, _ := strings.Cut(header, " (UTF-8 bytes ")
		var nextStart, nextEnd, nextTotal int
		n, _ := fmt.Sscanf(coordinates, "%d:%d of %d", &nextStart, &nextEnd, &nextTotal)
		if !ok || key != identity || n != 3 || nextStart != end || nextEnd-nextStart != len(body) || nextTotal != total || nextEnd > total {
			break
		}
		headers, bodies = append(headers, header), append(bodies, body)
		end = nextEnd
		if end == total {
			return headers, bodies
		}
	}
	return headers[:1], bodies[:1]
}

// Project only complete attachment frames, never ordinary prompts or failure
// receipts. Snapshot byte ranges remain those of the original submitted body.
func duplicateAttachmentBody(text string) (header, body string, ok bool) {
	if selection, selected := attachments.ParseSelection(text); selected && (selection.Footer != "" || strings.HasSuffix(selection.Header, "):\n")) {
		return selection.Header, selection.Body, true
	}
	first, body, found := strings.Cut(text, "\n")
	if !found || !strings.Contains(first, " (UTF-8 bytes ") ||
		!strings.HasSuffix(first, attachments.ReadGuidance+"):") ||
		!strings.HasPrefix(first, "Attached file ") && !attachments.SkillFrame(text) {
		return "", "", false
	}
	return first + "\n", body, true
}

// A negative count selects all user parts. Host counts exclude router appends.
func projectDuplicateTextParts(raw jsonv1.RawMessage, count int, role string, kinds []string, project func(string) string) (jsonv1.RawMessage, bool) {
	eligible := func(at int) bool {
		return role != "user" || at >= len(kinds) || kinds[at] == "" || strings.HasPrefix(kinds[at], "user.")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if role != "" {
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
		wanted := "input_text"
		if role == "assistant" {
			wanted = "output_text"
		}
		if kind != wanted || json.Unmarshal(part["text"], &text) != nil {
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

func repetitionLabel(lines, body, repeated int) string {
	if lines == 0 {
		return ""
	}
	if repeated == body {
		return "repeated"
	}
	return fmt.Sprintf("%d lines repeated", lines)
}

// mread may frame several handles in one output. Only complete validated
// envelopes establish attachment sources; surrounding host text stays verbatim.
func projectRetainedAttachments(text string, project func(string) string) (string, bool) {
	var result strings.Builder
	matched := false
	at := 0
	for {
		start := strings.Index(text[at:], "<mekugi-file-attachments-")
		if start < 0 {
			break
		}
		start += at
		close := strings.Index(text[start:], "\n</mekugi-file-attachments-")
		if close < 0 {
			break
		}
		close += start
		end := strings.IndexByte(text[close:], '>')
		if end < 0 {
			break
		}
		end += close + 1
		candidate := text[start:end]
		if _, valid := attachments.DecodeSnapshot(candidate); valid {
			result.WriteString(text[at:start])
			result.WriteString(project(candidate))
			at = end
			matched = true
		} else {
			result.WriteString(text[at : start+1])
			at = start + 1
		}
	}
	result.WriteString(text[at:])
	return result.String(), matched
}
