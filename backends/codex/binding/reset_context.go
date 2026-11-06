package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// ResetUserContext selects the native instruction/environment fragments used
// when estimating rebuilt context. User tasks, selected skills and media are
// not initial context. Explicit user-content provenance overrides wrappers.
func ResetUserContext(input jsontext.Value) ([]jsontext.Value, error) {
	var items []struct {
		Role     string         `json:"role"`
		Content  jsontext.Value `json:"content"`
		Metadata struct {
			Kinds []string `json:"content_item_kinds"`
		} `json:"internal_chat_message_metadata_passthrough"`
	}
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	var result []jsontext.Value
	type textPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	for _, item := range items {
		if item.Role != "user" {
			continue
		}
		var parts []textPart
		var text string
		if json.Unmarshal(item.Content, &text) == nil {
			parts = append(parts, textPart{"input_text", text})
		} else if json.Unmarshal(item.Content, &parts) != nil {
			continue
		}
		var selected []map[string]string
		for i, part := range parts {
			if part.Type != "text" && part.Type != "input_text" || len(item.Metadata.Kinds) == len(parts) && strings.HasPrefix(item.Metadata.Kinds[i], "user.") {
				continue
			}
			text := strings.TrimSpace(part.Text)
			if strings.HasPrefix(text, "# AGENTS.md instructions") && strings.HasSuffix(text, "</INSTRUCTIONS>") || strings.HasPrefix(text, "<environment_context>") && strings.HasSuffix(text, "</environment_context>") {
				selected = append(selected, map[string]string{"type": "input_text", "text": part.Text})
			}
		}
		if len(selected) > 0 {
			result = append(result, responses.MustMarshalJSON(map[string]any{"type": "message", "role": "user", "content": selected}))
		}
	}
	return result, nil
}
