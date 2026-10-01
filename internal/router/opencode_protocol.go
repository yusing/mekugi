package router

import (
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
)

// Provider reasoning is opaque replay data, not an OpenAI encrypted-history
// token. Bind the envelope to its service and model so model switches fail
// explicitly instead of replaying another provider's signature.
type openCodeReasoning struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Item     jsontext.Value `json:"item"`
}

const openCodeReasoningPrefix = "mekugi-opencode-v1:"

func sealOpenCodeReasoning(service *openCodeService, model string, item any) string {
	data := mustMarshalJSON(openCodeReasoning{Provider: service.prefix, Model: model, Item: mustMarshalJSON(item)})
	return openCodeReasoningPrefix + base64.RawStdEncoding.EncodeToString(data)
}

func restoreOpenCodeReasoning(service *openCodeService, model, value string) (map[string]any, error) {
	encoded, ok := strings.CutPrefix(value, openCodeReasoningPrefix)
	if !ok {
		return nil, incompatibleRequest("opencode_encrypted_history", "OpenCode cannot read foreign encrypted reasoning; start a fresh thread or use fork_turns=none.")
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	var envelope openCodeReasoning
	if err != nil || json.Unmarshal(data, &envelope) != nil || envelope.Provider != service.prefix || envelope.Model != model {
		return nil, incompatibleRequest("opencode_encrypted_history", "OpenCode reasoning belongs to another service or model; start a fresh thread.")
	}
	var item map[string]any
	if json.Unmarshal(envelope.Item, &item) != nil || item == nil {
		return nil, errors.New("invalid retained OpenCode reasoning")
	}
	kind, _ := item["type"].(string)
	if service.format(model) == "anthropic" && (kind == "thinking" || kind == "redacted_thinking") ||
		service.format(model) == "responses" && kind == "reasoning" {
		return item, nil
	}
	return nil, errors.New("invalid retained OpenCode reasoning type")
}
