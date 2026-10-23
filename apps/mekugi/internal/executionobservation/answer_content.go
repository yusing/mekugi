package executionobservation

import (
	"encoding/json"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func isFinalAnswerMessage(item map[string]json.RawMessage) bool {
	return responses.Facts(item).FinalAnswerCandidate()
}

// Source: codex-rs/protocol/src/models.rs ContentItem and MessagePhase.
// Codex drops messages with unsupported content (notably refusal parts).
// JSON and SSE must not make usage the only consumable assistant item.
func isSubstantiveAnswer(item map[string]json.RawMessage) bool {
	if !isFinalAnswerMessage(item) {
		return false
	}
	if status := responses.StringField(item, "status"); status != "" && status != "completed" {
		return false
	}
	if raw, present := item["phase"]; present {
		var phase *string
		if json.Unmarshal(raw, &phase) != nil || phase != nil && *phase != "final_answer" {
			return false
		}
	}
	var content []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(item["content"], &content) != nil {
		return false
	}
	substantive := false
	for _, part := range content {
		if part.Type != "output_text" || part.Text == nil {
			return false
		}
		substantive = substantive || strings.TrimSpace(*part.Text) != ""
	}
	return substantive

}

// Client dispatches and unfinished hosted calls are not final-answer turns.
func blocksTokenUsage(item map[string]json.RawMessage) bool {
	switch responses.StringField(item, "type") {
	case "function_call", "custom_tool_call", "computer_call", "local_shell_call", "apply_patch_call", "mcp_approval_request":
		return true
	case "tool_search_call":
		if responses.StringField(item, "execution") != "server" {
			return true
		}
	case "shell_call":
		var environment map[string]json.RawMessage
		if json.Unmarshal(item["environment"], &environment) != nil || responses.StringField(environment, "type") != "container_reference" {
			return true
		}
	}
	if strings.HasSuffix(responses.StringField(item, "type"), "_call") {
		status := responses.StringField(item, "status")
		return status != "completed" && status != "failed"
	}
	return false
}
