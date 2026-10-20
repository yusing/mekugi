package rollout

import json "encoding/json/v2"

// Source: internal/router/app_server_context.go:14:40@[bf0e408c] context evidence.
type ContextUsage struct{ Tokens, Window uint64 }

func ReadContextUsage(threadID, path string) (ContextUsage, bool) {
	for line := range reverseRecords(threadID, path) {
		var event struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info *struct {
					Last *struct {
						Total uint64 `json:"total_tokens"`
					} `json:"last_token_usage"`
					Window uint64 `json:"model_context_window"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "event_msg" || event.Payload.Type != "token_count" || event.Payload.Info == nil || event.Payload.Info.Last == nil {
			continue
		}
		return ContextUsage{Tokens: event.Payload.Info.Last.Total, Window: event.Payload.Info.Window}, true
	}
	return ContextUsage{}, false
}
