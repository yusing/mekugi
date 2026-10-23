package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/guidance"
)

func projectThreadInstructions(input binding.InstructionInput, replacements []guidance.Replacement) (binding.InstructionUpdate, error) {
	result := binding.InstructionUpdate{InstructionInput: input}
	if len(replacements) == 0 {
		return result, nil
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(input.Input, &items); err != nil {
		return result, err
	}
	for _, item := range items {
		var id, role string
		_ = json.Unmarshal(item["id"], &id)
		_ = json.Unmarshal(item["role"], &role)
		if role != "developer" || id == "" {
			continue
		}
		for _, replacement := range replacements {
			part := replacement.Fragment
			if part.Item != id {
				continue
			}
			var content []map[string]jsontext.Value
			if err := json.Unmarshal(item["content"], &content); err != nil {
				return result, err
			}
			if part.Index < 0 || part.Index >= len(content) {
				continue
			}
			var text string
			if err := json.Unmarshal(content[part.Index]["text"], &text); err != nil {
				return result, err
			}
			if text == replacement.Text {
				continue
			}
			if err := binding.MatchInstructionFragment(part, part.Thread, id, part.Index, text); err != nil {
				return result, err
			}
			content[part.Index]["text"], _ = json.Marshal(replacement.Text)
			var err error
			item["content"], err = json.Marshal(content)
			if err != nil {
				return result, err
			}
			result.InputChanged = true
		}
	}
	if result.InputChanged {
		var err error
		result.Input, err = json.Marshal(items)
		return result, err
	}
	return result, nil
}
