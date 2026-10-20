// Source: internal/router/instruction_conflicts.go:119:134@[7a069269] instruction carriers.
package codexbinding

import (
	"fmt"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func forExecution(input binding.InstructionInput, rewriteText func(string) string) (binding.InstructionUpdate, error) {
	request := binding.InstructionUpdate{InstructionInput: input}
	if original, ok := responses.DecodeString(request.Instructions); ok {
		if updated := rewriteText(original); updated != original {
			request.Instructions = responses.MustMarshalJSON(updated)
			request.InstructionsChanged = true
		}
	}
	err := rewriteRequestMessages(&request, "instruction conflicts", func(item responses.Item) ([]byte, bool, error) {
		if item.Role != "developer" {
			return nil, false, nil
		}
		content, found, err := transformResponsesTextContent(item.Content, rewriteText, isResponsesInputTextPart)
		if err != nil {
			return nil, false, fmt.Errorf("rewrite developer instruction conflicts: %w", err)
		}
		return content, found && string(content) != string(item.Content), nil
	})
	return request, err
}
