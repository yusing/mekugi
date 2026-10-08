package claude

import (
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

// Historical messages restore display only. No partial buffers, permission
// requests, running processes or edit baselines are reconstructed from replay.
func historyEvents(e nativeEvent) ([]session.Event, error) {
	if e.Type != "assistant" && e.Type != "user" {
		return nil, nil
	}
	message, err := e.message()
	if err != nil {
		return nil, err
	}
	role := "Claude"
	if e.Type == "user" {
		role = "You"
	}
	id := "history/" + e.UUID
	if message.Content.Kind() == '"' {
		text := contentText(message.Content)
		return []session.Event{{Kind: "message", ID: id, Role: role, Text: text, Historical: true}}, nil
	}
	var blocks []content
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		return nil, err
	}
	var result []session.Event
	if text := contentText(message.Content); text != "" {
		result = append(result, session.Event{Kind: "message", ID: id, Role: role, Text: text, Historical: true})
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			result = append(result, session.Event{Kind: "tool", ID: "history/" + b.ID, Role: b.Name, Text: string(b.Input), Historical: true})
		case "tool_result":
			text := contentText(b.Content)
			result = append(result, session.Event{Kind: "tool_result", ID: "history/" + b.ToolUseID, Text: text, Skill: savedSkillName(text), Failed: b.IsError, Historical: true})
		}
	}
	return result, nil
}

// Native Skill maps its resolved commandName into these tool-result forms.
// The caller must still pair this receipt with a successful native Skill call.
func savedSkillName(text string) string {
	if name, ok := strings.CutPrefix(text, "Launching skill: "); ok && !strings.ContainsAny(name, "\r\n") {
		return name
	}
	if rest, ok := strings.CutPrefix(text, `Skill "`); ok {
		name, suffix, found := strings.Cut(rest, `"`)
		if found && (strings.HasPrefix(suffix, " launched (forked execution, running in the background).\n\n") || strings.HasPrefix(suffix, " completed (forked execution).\n\nResult:\n")) {
			return name
		}
	}
	if rest, ok := strings.CutPrefix(text, "Loaded skill instructions (read-only): "); ok {
		name, found := strings.CutSuffix(rest, ". Nothing was executed; delegate execution to a worker.")
		if found {
			return name
		}
	}
	return ""
}
