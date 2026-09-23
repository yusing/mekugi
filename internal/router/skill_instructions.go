package router

import (
	"bytes"
	"encoding/xml"
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

// rewriteSelectedSkillInstructions replaces Codex's complete selected-skill
// injection with the skill identity consumed by skills-mgr guidance.
func rewriteSelectedSkillInstructions(text string) string {
	const (
		open      = "<skill>\n<name>"
		nameClose = "</name>\n"
		pathOpen  = "<path>"
		pathClose = "</path>\n"
		close     = "\n</skill>"
	)
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, open) || !strings.HasSuffix(trimmed, close) {
		return text
	}
	nameEnd := strings.Index(trimmed[len(open):], nameClose)
	if nameEnd < 0 {
		return text
	}
	pathStart := len(open) + nameEnd + len(nameClose)
	if !strings.HasPrefix(trimmed[pathStart:], pathOpen) {
		return text
	}
	pathEnd := strings.Index(trimmed[pathStart+len(pathOpen):], pathClose)
	if pathEnd < 0 {
		return text
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(trimmed[len(open):len(open)+nameEnd])); err != nil {
		return text
	}
	rewritten := `<skill name="` + escaped.String() + `"/>`
	start := strings.Index(text, trimmed)
	return text[:start] + rewritten + text[start+len(trimmed):]
}

func rewriteRequestSelectedSkillInstructions(request *parsedResponsesRequest, enabled bool) error {
	if !enabled {
		return nil
	}
	if len(request.fields["input"]) == 0 {
		return nil
	}
	input, err := decodeResponsesInput(request.fields["input"])
	if err != nil {
		return fmt.Errorf("decode selected skill instructions: %w", err)
	}
	changed := false
	for index, raw := range input.items {
		item, ok := decodeResponsesItem(raw)
		if !ok || item.Role != "user" || item.Type != "" && item.Type != "message" {
			continue
		}
		textChanged := false
		content, _, err := transformResponsesTextContent(item.Content, func(text string) string {
			rewritten := rewriteSelectedSkillInstructions(text)
			textChanged = textChanged || rewritten != text
			return rewritten
		}, isResponsesInputTextPart)
		if err != nil {
			return fmt.Errorf("rewrite selected skill instructions: %w", err)
		}
		if !textChanged {
			continue
		}
		item.setContent(content)
		input.items[index] = mustMarshalJSON(item)
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := input.encode()
	if err != nil {
		return fmt.Errorf("encode selected skill instructions: %w", err)
	}
	request.setInput(encoded)
	return nil
}
