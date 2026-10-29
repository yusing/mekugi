package codexbinding

import (
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/contextnotice"
)

func contextKey(index int, item map[string]jsonv1.RawMessage) string {
	if id := responses.StringField(item, "call_id"); id != "" {
		return responses.StringField(item, "type") + ":" + id
	}
	return fmt.Sprintf("%d:%x", index, sha256.Sum256(append(append([]byte(nil), item["content"]...), item["output"]...)))
}

func projectContextUsage(input binding.InstructionInput, admitted binding.ContextUsage, store *contextnotice.Store) (binding.InstructionUpdate, error) {
	update := binding.InstructionUpdate{InstructionInput: input}
	var items []map[string]jsonv1.RawMessage
	var err error
	_, marks := store.Project(admitted.Session, admitted.Thread, contextnotice.Sample{Tokens: admitted.Tokens, Window: admitted.Window, Compactions: admitted.Compactions}, func(last string) contextnotice.Mark {
		target := contextnotice.Mark{Index: -1}
		if err = json.Unmarshal(input.Input, &items); err != nil {
			return target
		}
		fresh := items
		if admitted.FreshInput != nil {
			fresh = nil
			err = json.Unmarshal(admitted.FreshInput(), &fresh)
			if err != nil {
				return target
			}
		}
		tool := false
		for index, item := range fresh {
			kind := responses.StringField(item, "type")
			isTool := kind == "function_call_output" || kind == "custom_tool_call_output"
			if isTool || responses.StringField(item, "role") == "user" && (!tool || admitted.FreshInput == nil) {
				target = contextnotice.Mark{Index: index, Key: contextKey(index, item)}
				tool = isTool
			}
		}
		if admitted.FreshInput != nil && target.Index >= 0 {
			for index := len(items) - 1; index >= 0; index-- {
				if tool && contextKey(target.Index, items[index]) == target.Key || !tool && responses.StringField(items[index], "role") == "user" {
					return contextnotice.Mark{Index: index, Key: contextKey(index, items[index])}
				}
			}
			return contextnotice.Mark{Index: -1}
		}
		if target.Key == last {
			target.Index = -1
		}
		return target
	})
	if err != nil || len(marks) == 0 {
		return update, err
	}
	if items == nil {
		if err := json.Unmarshal(input.Input, &items); err != nil {
			return update, err
		}
	}
	projected := make([]map[string]jsonv1.RawMessage, 0, len(items)+len(marks))
	for index, item := range items {
		projected = append(projected, item)
		for _, mark := range marks {
			if index == mark.Index && contextKey(index, item) == mark.Key && (index+1 == len(items) || responses.StringField(items[index+1], "content") != mark.Text) {
				projected = append(projected, map[string]jsonv1.RawMessage{"type": responses.MustMarshalJSON("message"), "role": responses.MustMarshalJSON("developer"), "content": responses.MustMarshalJSON(mark.Text)})
				update.InputChanged = true
			}
		}
	}
	if update.InputChanged {
		update.Input = responses.MustMarshalJSON(projected)
	}
	return update, nil
}
