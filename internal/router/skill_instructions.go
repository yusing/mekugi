package router

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func skillsManagerInPath(extraDirectory string) bool {
	if path, err := exec.LookPath("skills-mgr"); err == nil || path != "" && errors.Is(err, exec.ErrDot) {
		return true
	}
	if extraDirectory == "" {
		return false
	}
	_, err := exec.LookPath(filepath.Join(extraDirectory, "skills-mgr"))
	return err == nil
}

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

func rewriteRequestSelectedSkillInstructions(request *parsedResponsesRequest, enabled bool) error {
	if !enabled {
		return nil
	}
	return rewriteRequestMessages(request, "selected skill instructions", func(item responsesItem) ([]byte, bool, error) {
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
}
