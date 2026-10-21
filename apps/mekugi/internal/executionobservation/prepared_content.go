package executionobservation

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// ObservePreparedContent receives the final semantic input before transport
// history compression. Attachment indices come from the projection that emitted
// those messages; subsequent skill rewriting preserves message membership.
func (t *mekugiResponseTransform) ObservePreparedContent(request *codexresponses.Request, attachmentIDs map[int]string) {
	var items []map[string]jsontext.Value
	if t.activity == nil || t.threadID == "" || json.Unmarshal(request.Fields["input"], &items) != nil {
		return
	}
	sources := make(map[string]string)
	originalOutputs := make(map[string]string)
	originalAttachments := make(map[string][]string)
	var originals []map[string]jsontext.Value
	_ = json.Unmarshal(request.OriginalField("input"), &originals)
	for _, item := range originals {
		kind := responses.StringField(item, "type")
		if kind == "custom_tool_call_output" || kind == "function_call_output" {
			if text, ok := preparedOutputText(item["output"]); ok {
				originalOutputs[kind+"\x00"+responses.StringField(item, "call_id")] = text
			}
		}
		if responses.StringField(item, "role") == "user" {
			var parts []map[string]jsontext.Value
			if json.Unmarshal(item["content"], &parts) == nil {
				for _, part := range parts {
					if responses.StringField(part, "type") != "input_text" {
						continue
					}
					if snapshot, ok := attachments.DecodeSnapshot(responses.StringField(part, "text")); ok {
						for _, frame := range snapshot.Frames {
							id := snapshot.ReceiptID(frame)
							originalAttachments[id] = append(originalAttachments[id], frame)
						}
					}
				}
			}
		}
		if responses.StringField(item, "type") == "custom_tool_call" && responses.StringField(item, "name") == t.codeModeToolName {
			sources[responses.StringField(item, "call_id")] = responses.StringField(item, "input")
		}
	}
	attached := make(map[string][]string)
	for i, item := range items {
		if id := attachmentIDs[i]; id != "" {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(item["content"], &parts) == nil {
				for _, part := range parts {
					if part.Type == "input_text" {
						attached[id] = append(attached[id], part.Text)
					}
				}
			}
		}
		kind := responses.StringField(item, "type")
		if kind != "custom_tool_call_output" && kind != "function_call_output" {
			continue
		}
		id := responses.StringField(item, "call_id")
		text, readable := preparedOutputText(item["output"])
		original, known := originalOutputs[kind+"\x00"+id]
		content := activity.PreparedContent{ID: id, Text: text, Altered: readable && known && text != original}
		if source, exists := sources[id]; exists {
			content.Cell = true
			if history, ok := t.visible[id]; ok {
				if history.CarrierPayload != "" {
					source = history.CarrierPayload
				} else if history.Script != "" {
					source = history.Script
				}
			}
			content.Calls = t.nativeTrace.ReadCell(t.threadID, id, source).ToolCalls()
		}
		t.activity.ObservePrepared(t.threadID, content)
	}
	for id, frames := range attached {
		text := strings.Join(frames, "\n\n")
		original, known := originalAttachments[id]
		t.activity.ObservePrepared(t.threadID, activity.PreparedContent{ID: id, Text: text, Altered: known && text != strings.Join(original, "\n\n")})
	}
}

// preparedOutputText removes only recognized transport text wrappers. JSON printed
// by a tool is literal text; mixed media and extended objects retain their facts.
func preparedOutputText(raw jsontext.Value) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw.Kind() == '"' {
		var text string
		err := json.Unmarshal(raw, &text)
		return text, err == nil
	}
	if raw.Kind() == '[' {
		var parts []jsontext.Value
		if json.Unmarshal(raw, &parts) != nil {
			return "", false
		}
		texts := make([]string, 0, len(parts))
		hasText := false
		for _, part := range parts {
			var fields map[string]jsontext.Value
			var text string
			if json.Unmarshal(part, &fields) == nil && len(fields) == 2 &&
				responses.StringField(fields, "type") == "input_text" && json.Unmarshal(fields["text"], &text) == nil {
				hasText = true
				texts = append(texts, text)
			} else {
				texts = append(texts, string(part))
			}
		}
		if !hasText {
			return string(raw), true
		}
		return strings.Join(texts, "\n"), true
	}
	return string(raw), true
}
