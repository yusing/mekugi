// Source: internal/router/instruction_omit.go:72:98@[7a069269] instruction carriers.
package codexbinding

import (
	"fmt"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

func beforePreparation(input binding.InstructionInput) (binding.InstructionUpdate, error) {
	request := binding.InstructionUpdate{InstructionInput: input}
	if text, ok := responses.DecodeString(request.Instructions); ok {
		if stripped := policy.Omissions(text); stripped != text {
			request.Instructions = responses.MustMarshalJSON(stripped)
			request.InstructionsChanged = true
		}
	}
	err := rewriteRequestMessages(&request, "instruction omissions", func(item responses.Item) ([]byte, bool, error) {
		transform := policy.Omissions
		switch item.Role {
		case "system", "developer":
		case "user":
			transform = policy.UserOmissions
		default:
			return nil, false, nil
		}
		textChanged := false
		content, _, err := transformResponsesTextContent(item.Content, func(text string) string {
			stripped := transform(text)
			textChanged = textChanged || stripped != text
			return stripped
		}, isResponsesInputTextPart)
		if err != nil {
			return nil, false, fmt.Errorf("strip instruction omissions: %w", err)
		}
		return content, textChanged, nil
	})
	return request, err
}
