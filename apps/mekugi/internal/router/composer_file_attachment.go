package router

import (
	"bytes"
	"encoding/json/jsontext"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

const composerTextLimit = 1 << 20 // Codex MAX_USER_INPUT_TEXT_CHARS; bytes are conservative.

// projectFileAttachments splits only complete versioned text parts. Malformed
// lookalikes remain ordinary user text; non-user messages are never interpreted.
func projectFileAttachments(request *codexresponses.Request) bool {
	if !bytes.Contains(request.Fields["input"], []byte("mekugi-file-attachments-v1")) {
		return false
	}
	var items []map[string]jsontext.Value
	if json.Unmarshal(request.Fields["input"], &items) != nil {
		return false
	}
	var projected []map[string]jsontext.Value
	changed := false
	attachedSkills := make(map[string]bool)
	for _, item := range items {
		var role string
		_ = json.Unmarshal(item["role"], &role)
		var parts []map[string]jsontext.Value
		if role != "user" || json.Unmarshal(item["content"], &parts) != nil {
			continue
		}
		for _, part := range parts {
			var kind, text string
			_ = json.Unmarshal(part["type"], &kind)
			_ = json.Unmarshal(part["text"], &text)
			if kind != "input_text" {
				continue
			}
			frames, _ := attachments.Decode(text)
			for _, frame := range frames {
				if name, path, rest := attachments.ParseSkillFrame(frame); name != "" && strings.HasPrefix(rest, " (UTF-8 bytes ") {
					attachedSkills[name+"\x00"+path] = true
				}
			}
		}
	}
	isAttachedSkill := func(text string) bool {
		name, path := attachments.SelectedSkillSource(text)
		return name != "" && (attachedSkills[name+"\x00"] || path != "" && attachedSkills[name+"\x00"+path])
	}
	for _, item := range items {
		var role string
		_ = json.Unmarshal(item["role"], &role)
		var selectedText string
		if role == "user" && json.Unmarshal(item["content"], &selectedText) == nil && isAttachedSkill(selectedText) {
			changed = true
			continue
		}
		var parts []map[string]jsontext.Value
		if role != "user" || json.Unmarshal(item["content"], &parts) != nil {
			projected = append(projected, item)
			continue
		}
		var kept []map[string]jsontext.Value
		var attached []string
		for _, part := range parts {
			var kind, text string
			_ = json.Unmarshal(part["type"], &kind)
			_ = json.Unmarshal(part["text"], &text)
			// Codex can independently inject the same skill from $name. The
			// durable snapshot already supplies its selected instructions.
			if kind == "input_text" && isAttachedSkill(text) {
				changed = true
				continue
			}
			frames, ok := attachments.Decode(text)
			if kind == "input_text" && ok {
				attached = append(attached, frames...)
			} else {
				kept = append(kept, part)
			}
		}
		if len(attached) == 0 {
			if len(kept) == 0 {
				continue
			}
			if len(kept) != len(parts) {
				item["content"], _ = json.Marshal(kept)
				delete(item, "internal_chat_message_metadata_passthrough")
			}
			projected = append(projected, item)
			continue
		}
		changed = true
		if len(kept) > 0 {
			item["content"], _ = json.Marshal(kept)
			// Content-kind metadata refers to the original part indices.
			delete(item, "internal_chat_message_metadata_passthrough")
			projected = append(projected, item)
		}
		for _, frame := range attached {
			content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": frame}})
			projected = append(projected, map[string]jsontext.Value{"type": []byte(`"message"`), "role": []byte(`"user"`), "content": content})
		}
	}
	if changed {
		input, _ := json.Marshal(projected)
		request.Fields["input"] = input
	}
	return changed
}
