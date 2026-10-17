// Source: apps/mekugi/internal/router/subagent_commentary.go@[d6415ec1].
package binding

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// SubagentSettings contains only settings observed on a child request or rollout.
type SubagentSettings struct{ Model, Effort, Tier string }

// SubagentMessageID preserves supplemental observation identity across live and restored input.
func SubagentMessageID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("msg_mekugi_subagent_commentary_%x", digest[:12])
}

// SubagentStart validates observed settings; a start without its model is unknown.
func SubagentStart(model, effort, tier string) *SubagentSettings {
	if tier == "priority" {
		tier = "fast"
	}
	effort = strings.TrimSpace(effort)
	if model == "" || len(model)+len(effort)+len(tier) > 16<<10 || strings.ContainsAny(model+effort+tier, "\r\n\x00") {
		return nil
	}
	return &SubagentSettings{Model: model, Effort: effort, Tier: tier}
}

// SubagentResponse decodes native replies, reporting opaque payloads only as receipts.
func SubagentResponse(item map[string]json.RawMessage) (text, sender string, final, ok bool) {
	if responses.StringField(item, "type") != "agent_message" {
		return "", "", false, false
	}
	sender = responses.StringField(item, "author")
	if sender != "/root" && !strings.HasPrefix(sender, "/root/") || strings.ContainsAny(sender, "\r\n\x00") {
		return "", "", false, false
	}
	var content []map[string]json.RawMessage
	if json.Unmarshal(item["content"], &content) != nil {
		return "", "", false, false
	}
	// Native Codex pairs the plaintext routing header with opaque ciphertext.
	// Report receipt only; neither part is a plaintext reply payload.
	if len(content) == 2 && responses.StringField(content[0], "type") == "input_text" &&
		responses.StringField(content[1], "type") == "encrypted_content" {
		return "", sender, false, true
	}
	if len(content) != 1 {
		return "", "", false, false
	}
	if responses.StringField(content[0], "type") == "encrypted_content" {
		return "", sender, false, true
	}
	if responses.StringField(content[0], "type") != "input_text" {
		return "", "", false, false
	}
	body := responses.StringField(content[0], "text")
	header, payload, found := strings.Cut(body, "\nPayload:\n")
	if !found || (!strings.HasPrefix(header, "Message Type: MESSAGE\n") && !strings.HasPrefix(header, "Message Type: FINAL_ANSWER\n")) {
		return "", "", false, false
	}
	return payload, sender, strings.HasPrefix(header, "Message Type: FINAL_ANSWER\n"), true
}
