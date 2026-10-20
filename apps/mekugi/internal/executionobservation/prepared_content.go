package executionobservation

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
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
	var originals []map[string]jsontext.Value
	_ = json.Unmarshal(request.OriginalField("input"), &originals)
	for _, item := range originals {
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
		content := activity.PreparedContent{ID: id, Text: string(item["output"])}
		if item["output"].Kind() == '"' {
			_ = json.Unmarshal(item["output"], &content.Text)
		}
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
		t.activity.ObservePrepared(t.threadID, activity.PreparedContent{ID: id, Text: strings.Join(frames, "\n\n")})
	}
}
