// Source: internal/router/skill_instructions.go:24:58@[7a069269] selected-skill projection.
package codexbinding

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// rewriteSelectedSkillInstructions replaces Codex's complete selected-skill
// injection with the skill identity consumed by skills-mgr guidance.
func rewriteSelectedSkillInstructions(text string) string {
	name, _ := attachments.SelectedSkillInstructions(text)
	if name == "" {
		return text
	}
	trimmed := strings.TrimSpace(text)
	rewritten := attachments.ManagedSkillReference(name)
	start := strings.Index(text, trimmed)
	return text[:start] + rewritten + text[start+len(trimmed):]
}

func afterAttachments(input binding.InstructionInput, enabled bool) (binding.InstructionUpdate, error) {
	request := binding.InstructionUpdate{InstructionInput: input}
	if !enabled {
		return request, nil
	}
	err := rewriteRequestMessages(&request, "selected skill instructions", func(item responses.Item) ([]byte, bool, error) {
		if item.Role != "user" {
			return nil, false, nil
		}
		textChanged := false
		content, _, err := transformResponsesTextContent(item.Content, func(text string) string {
			rewritten := rewriteSelectedSkillInstructions(text)
			textChanged = textChanged || rewritten != text
			return rewritten
		}, isResponsesInputTextPart)
		if err != nil {
			return nil, false, fmt.Errorf("rewrite selected skill instructions: %w", err)
		}
		return content, textChanged, nil
	})
	return request, err
}
