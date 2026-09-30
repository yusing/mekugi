package router

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

func selectedSkillInstructions(text string) (name, path string) {
	const (
		open      = "<skill>\n<name>"
		nameClose = "</name>\n"
		pathOpen  = "<path>"
		pathClose = "</path>\n"
		close     = "\n</skill>"
	)
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, open) || !strings.HasSuffix(trimmed, close) {
		return "", ""
	}
	nameEnd := strings.Index(trimmed[len(open):], nameClose)
	if nameEnd < 0 {
		return "", ""
	}
	pathStart := len(open) + nameEnd + len(nameClose)
	if !strings.HasPrefix(trimmed[pathStart:], pathOpen) {
		return "", ""
	}
	pathEnd := strings.Index(trimmed[pathStart+len(pathOpen):], pathClose)
	if pathEnd < 0 {
		return "", ""
	}
	return trimmed[len(open) : len(open)+nameEnd], trimmed[pathStart+len(pathOpen) : pathStart+len(pathOpen)+pathEnd]
}

// rewriteSelectedSkillInstructions replaces Codex's complete selected-skill
// injection with the skill identity consumed by skills-mgr guidance.
func rewriteSelectedSkillInstructions(text string) string {
	name, _ := selectedSkillInstructions(text)
	if name == "" {
		return text
	}
	trimmed := strings.TrimSpace(text)
	rewritten := managedSkillReference(name)
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
