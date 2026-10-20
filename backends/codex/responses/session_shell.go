package responses

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"html"
	"strings"
)

// Source: apps/mekugi/internal/router/exec_observation.go:23:74@874a6e86.
// Environment carriers preserve the host protocol's permissive JSON decoding.
// SessionShell reads the latest session environment shell. Multiple environments with different shells leave it
// unknown.
func SessionShell(raw jsonv1.RawMessage) string {
	var input []struct {
		Type    string            `json:"type"`
		Content jsonv1.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &input, jsonv1.DefaultOptionsV1()) != nil {
		return ""
	}
	shell := ""
	for _, item := range input {
		if item.Type != "message" && item.Type != "" {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &parts, jsonv1.DefaultOptionsV1()) != nil {
			continue
		}
		for _, part := range parts {
			text := strings.TrimSpace(part.Text)
			if !strings.HasPrefix(text, "<environment_context>") || !strings.HasSuffix(text, "</environment_context>") {
				continue
			}
			var shells []string
			for rest := text; ; {
				_, after, found := strings.Cut(rest, "<shell>")
				if !found {
					break
				}
				value, remainder, closed := strings.Cut(after, "</shell>")
				if !closed {
					break
				}
				shells, rest = append(shells, html.UnescapeString(value)), remainder
			}
			if len(shells) == 0 {
				// An update that omits the shell keeps the previous one.
				continue
			}
			shell = shells[0]
			for _, other := range shells[1:] {
				if other != shell {
					shell = ""
				}
			}
		}
	}
	return shell
}
