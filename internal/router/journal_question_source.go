package router

import (
	"encoding/json"
	"strings"
)

// journalQuestionFromInput uses only the reconstructed request view. It must not
// consult a thread-wide "last question" cache: steering and forks have distinct
// visible histories, and a newer media-only message must not select an older one.
func journalQuestionFromInput(raw json.RawMessage) string {
	input, err := decodeResponsesInput(raw)
	if err != nil {
		return ""
	}
	if input.text != nil {
		return journalUserText(*input.text)
	}
	for index := len(input.items) - 1; index >= 0; index-- {
		item, ok := decodeResponsesItem(input.items[index])
		if !ok || item.Role != "user" || item.Type != "" && item.Type != "message" {
			continue
		}
		if text, ok := decodeJSONString(item.Content); ok {
			if !journalContextText(text) {
				return journalUserText(text)
			}
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &parts) != nil {
			return ""
		}
		var metadata struct {
			Kinds []string `json:"content_item_kinds"`
		}
		_ = json.Unmarshal(item.fields["internal_chat_message_metadata_passthrough"], &metadata)
		var text []string
		userContent := false
		for index, part := range parts {
			kind := ""
			if len(metadata.Kinds) == len(parts) {
				kind = metadata.Kinds[index]
			}
			switch kind {
			case "images.preparation_error", "images.unsupported", "audio.unsupported":
				// Media preparation replaces actual input. It is not a new
				// textual question, but prevents falling back to an older one.
				userContent = true
				continue
			}
			if kind != "" && kind != "unknown" && !strings.HasPrefix(kind, "user.") {
				continue
			}
			if part.Type == "input_text" || part.Type == "text" {
				if !strings.HasPrefix(kind, "user.") && journalContextText(part.Text) {
					continue
				}
				text = append(text, part.Text)
			}
			userContent = true
		}
		if userContent || len(parts) == 0 {
			return journalUserText(strings.Join(text, "\n"))
		}
	}
	return ""
}

// Legacy request items lack content classifications. Match complete known host
// wrappers, not arbitrary mentions in a user's question. Modern classifications
// above take precedence, including user text that quotes one of these wrappers.
// Source: codex-rs/core/src/context/contextual_user_message.rs
// is_contextual_user_fragment and its ContextualUserFragment implementations.
func journalContextText(text string) bool {
	text = strings.TrimSpace(text)
	for _, pair := range [][2]string{
		{"# AGENTS.md instructions", "</INSTRUCTIONS>"},
		{"<environment_context>", "</environment_context>"},
		{"<turn_aborted>", "</turn_aborted>"},
		{"<user_shell_command>", "</user_shell_command>"},
		{"<subagent_notification>", "</subagent_notification>"},
		{"<codex_internal_context source=\"", "</codex_internal_context>"},
		{"<goal_context>", "</goal_context>"},
		{"<recommended_plugins>", "</recommended_plugins>"},
		{"<INSTRUCTIONS>", "</INSTRUCTIONS>"},
	} {
		if strings.HasPrefix(text, pair[0]) && strings.HasSuffix(text, pair[1]) {
			return true
		}
	}
	return false
}

// Source: codex-rs/protocol/src/protocol.rs:141:148 strip_user_message_prefix.
// Codex can prepend editor context to the actual request in the same text part.
func journalUserText(text string) string {
	if _, request, ok := strings.Cut(text, "## My request for Codex:"); ok {
		return strings.TrimSpace(request)
	}
	return text
}
