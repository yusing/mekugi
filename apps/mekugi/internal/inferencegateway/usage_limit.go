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

// UsageLimit decodes provider rejection facts, including HTTP and wrapped
// WebSocket timing. An absent deadline never authorizes a scheduled retry.
func UsageLimit(payload []byte, headers http.Header, now time.Time) (deadline *time.Time, limited bool) {
	if !bytes.Contains(payload, []byte("usage_limit_reached")) {
		return nil, false
	}
	var envelope struct {
		Error struct {
			Type     string                    `json:"type"`
			Code     string                    `json:"code"`
			ResetsAt *int64                    `json:"resets_at"`
			Headers  map[string]jsontext.Value `json:"headers"`
		} `json:"error"`
		Response jsontext.Value            `json:"response"`
		Headers  map[string]jsontext.Value `json:"headers"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, false
	}
	if len(envelope.Response) != 0 && json.Unmarshal(envelope.Response, &envelope) != nil {
		return nil, false
	}
	if envelope.Error.Type != "usage_limit_reached" && envelope.Error.Code != "usage_limit_reached" {
		return nil, false
	}
	if reset := envelope.Error.ResetsAt; reset != nil && *reset > 0 {
		return new(time.Unix(*reset, 0)), true
	}
	retry := headers.Get("Retry-After")
	for _, fields := range []map[string]jsontext.Value{envelope.Headers, envelope.Error.Headers} {
		for name, value := range fields {
			if strings.EqualFold(name, "Retry-After") {
				var advice string
				if json.Unmarshal(value, &advice) == nil {
					retry = advice
				}
			}
		}
	}
	if seconds, err := strconv.ParseInt(retry, 10, 32); err == nil && seconds >= 0 {
		return new(now.Add(time.Duration(seconds) * time.Second)), true
	}
	if at, err := http.ParseTime(retry); err == nil {
		return &at, true
	}
	return nil, true
}
