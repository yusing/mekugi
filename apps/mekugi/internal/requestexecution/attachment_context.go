package requestexecution

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// OmitCompactedAttachments removes pre-boundary supplied bodies from summary
// continuations. Native placeholders remain; optional recall owns restoration.
func OmitCompactedAttachments(request *codexresponses.Request, context []composer.ContextAttachment) error {
	if len(context) == 0 {
		return nil
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return err
	}
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
				for _, value := range context {
					if !value.Matches(snapshot, text) {
						continue
					}
					matched, changed = true, true
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
	request.Fields["input"] = responses.MustMarshalJSON(retained)
	return nil
}
