package attachments

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// Source: internal/router/app_server_text_spans.go:16:39@57f4b4c6
// Source: internal/router/skill_instructions.go:22:48@57f4b4c6
// Managed skill identities expose a name, not a Codex filesystem path.
func ManagedSkillReference(name string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(name))
	return `<skill name="` + escaped.String() + `"/>`
}

func SelectedSkillInstructions(text string) (name, path string) {
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
