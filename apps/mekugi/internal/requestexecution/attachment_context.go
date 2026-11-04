package requestexecution

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// RestoreAttachments projects only pre-boundary attachment context. Summary
// compaction may already retain its envelope; reset may remove it entirely.
// In both cases file bodies and stale skill bodies stay out of the new window.
func RestoreAttachments(request *codexresponses.Request, context []composer.ContextAttachment) error {
	if len(context) == 0 {
		return nil
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return err
	}
	present := make(map[int]bool)
	staleSkill := func(text string) bool {
		name, path := attachments.SelectedSkillSource(text)
		if name == "" {
			return false
		}
		for _, value := range context {
			for _, skill := range value.Skills {
				if skill.Name == name && (path == "" || skill.Path == "" || path == skill.Path) {
					return true
				}
			}
		}
		return false
	}
	var retained []map[string]jsontext.Value
	for _, item := range items {
		if responses.StringField(item, "role") != "user" {
			retained = append(retained, item)
			continue
		}
		if text := responses.StringField(item, "content"); text != "" && staleSkill(text) {
			continue
		}
		var parts []map[string]jsontext.Value
		if json.Unmarshal(item["content"], &parts) != nil {
			retained = append(retained, item)
			continue
		}
		var kept []map[string]jsontext.Value
		changed := false
		for _, part := range parts {
			text := responses.StringField(part, "text")
			if responses.StringField(part, "type") == "input_text" && staleSkill(text) {
				changed = true
				continue
			}
			snapshot, valid := attachments.DecodeSnapshot(text)
			matched := false
			if responses.StringField(part, "type") == "input_text" && valid {
				for i, value := range context {
					if !value.Matches(snapshot, text) {
						continue
					}
					present[i], matched, changed = true, true, true
					if len(value.Selection.Frames) > 0 {
						part["text"] = responses.MustMarshalJSON(value.Selection.Encode())
						kept = append(kept, part)
					}
					break
				}
			}
			if !matched {
				kept = append(kept, part)
			}
		}
		if changed {
			if len(kept) == 0 {
				continue
			}
			item["content"] = responses.MustMarshalJSON(kept)
			delete(item, "internal_chat_message_metadata_passthrough")
		}
		retained = append(retained, item)
	}
	var restored []map[string]jsontext.Value
	for i, value := range context {
		if present[i] {
			continue
		}
		var parts []map[string]string
		if len(value.Placeholders) > 0 {
			parts = append(parts, map[string]string{"type": "input_text", "text": strings.Join(value.Placeholders, " ")})
		}
		if len(value.Selection.Frames) > 0 {
			parts = append(parts, map[string]string{"type": "input_text", "text": value.Selection.Encode()})
		}
		if len(parts) > 0 {
			restored = append(restored, map[string]jsontext.Value{"type": responses.MustMarshalJSON("message"), "role": responses.MustMarshalJSON("user"), "content": responses.MustMarshalJSON(parts)})
		}
	}
	request.Fields["input"] = responses.MustMarshalJSON(append(restored, retained...))
	return nil
}
