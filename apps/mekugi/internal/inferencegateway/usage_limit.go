package inferencegateway

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProviderWait decodes usage-limit and capacity rejections and provider timing.
// Capacity permits bounded fallback timing; untimed usage limits remain paused.
func ProviderWait(payload []byte, headers http.Header, now time.Time) (deadline *time.Time, limited, capacity bool) {
	if !bytes.Contains(payload, []byte(`"error"`)) {
		return nil, false, false
	}
	var envelope struct {
		Error struct {
			Type     string                    `json:"type"`
			Code     string                    `json:"code"`
			Message  string                    `json:"message"`
			Provider string                    `json:"provider_error"`
			ResetsAt *int64                    `json:"resets_at"`
			Headers  map[string]jsontext.Value `json:"headers"`
		} `json:"error"`
		Response jsontext.Value            `json:"response"`
		Headers  map[string]jsontext.Value `json:"headers"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, false, false
	}
	if len(envelope.Response) != 0 && json.Unmarshal(envelope.Response, &envelope) != nil {
		return nil, false, false
	}
	if provider := envelope.Error.Provider; provider != "" {
		payload := []byte(provider)
		var nested struct {
			Error    jsontext.Value            `json:"error"`
			Response jsontext.Value            `json:"response"`
			Headers  map[string]jsontext.Value `json:"headers"`
		}
		if json.Unmarshal(payload, &nested) != nil {
			return nil, false, false
		}
		if len(nested.Response) != 0 && json.Unmarshal(nested.Response, &nested) != nil {
			return nil, false, false
		}
		if len(nested.Error) != 0 {
			payload = nested.Error
		}
		if len(nested.Headers) != 0 {
			envelope.Headers = nested.Headers
		}
		if json.Unmarshal(payload, &envelope.Error) != nil {
			return nil, false, false
		}
	}
	for _, kind := range []string{envelope.Error.Type, envelope.Error.Code} {
		switch kind {
		case "usage_limit_reached":
			limited = true
		case "overloaded_error", "server_is_overloaded", "server_overloaded", "model_overloaded", "model_at_capacity":
			limited, capacity = true, true
		case "service_unavailable_error", "server_error":
			message := strings.ToLower(envelope.Error.Message)
			if strings.Contains(message, "overloaded") || strings.Contains(message, "model is at capacity") {
				limited, capacity = true, true
			}
		}
	}
	if !limited {
		return nil, false, false
	}
	if reset := envelope.Error.ResetsAt; reset != nil && *reset > 0 {
		return new(time.Unix(*reset, 0)), true, capacity
	}
	retry := headers.Get("Retry-After")
	for _, fields := range []map[string]jsontext.Value{envelope.Headers, envelope.Error.Headers} {
		for name, value := range fields {
			if strings.EqualFold(name, "Retry-After") {
				var advice string
				if json.Unmarshal(value, &advice) == nil {
					retry = advice
				} else if value.Kind() == '0' {
					retry = string(value)
				}
			}
		}
	}
	retry = strings.TrimSpace(retry)
	if seconds, err := strconv.ParseInt(retry, 10, 32); err == nil && seconds >= 0 {
		return new(now.Add(time.Duration(seconds) * time.Second)), true, capacity
	}
	if at, err := http.ParseTime(retry); err == nil {
		return &at, true, capacity
	}
	return nil, true, capacity
}
