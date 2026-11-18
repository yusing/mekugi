package inferencegateway

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
)

// IsProviderLimitRejection identifies usage-limit and capacity rejections.
func IsProviderLimitRejection(payload []byte) bool {
	if !bytes.Contains(payload, []byte(`"error"`)) {
		return false
	}
	var envelope struct {
		Error struct {
			Type     string `json:"type"`
			Code     string `json:"code"`
			Message  string `json:"message"`
			Provider string `json:"provider_error"`
		} `json:"error"`
		Response jsontext.Value `json:"response"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return false
	}
	if len(envelope.Response) != 0 && json.Unmarshal(envelope.Response, &envelope) != nil {
		return false
	}
	if provider := envelope.Error.Provider; provider != "" {
		payload := []byte(provider)
		var nested struct {
			Error    jsontext.Value `json:"error"`
			Response jsontext.Value `json:"response"`
		}
		if json.Unmarshal(payload, &nested) != nil {
			return false
		}
		if len(nested.Response) != 0 && json.Unmarshal(nested.Response, &nested) != nil {
			return false
		}
		if len(nested.Error) != 0 {
			payload = nested.Error
		}
		if json.Unmarshal(payload, &envelope.Error) != nil {
			return false
		}
	}
	for _, kind := range []string{envelope.Error.Type, envelope.Error.Code} {
		switch kind {
		case "usage_limit_reached", "overloaded_error", "server_is_overloaded", "server_overloaded", "model_overloaded", "model_at_capacity":
			return true
		case "service_unavailable_error", "server_error":
			message := strings.ToLower(envelope.Error.Message)
			if strings.Contains(message, "overloaded") || strings.Contains(message, "model is at capacity") {
				return true
			}
		}
	}
	return false
}
