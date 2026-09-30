package router

import (
	json "encoding/json/v2"
	"time"
)

// Read only host-selected, identity-validated evidence, before resume writes new
// defaults. Settings events also preserve idle changes made after the last turn.
// Source: codex-rs/protocol/src/protocol.rs@68e1a421
// ThreadSettingsAppliedEvent, ThreadSettingsSnapshot and TurnContextItem.
func readResumeSettings(info appServerThreadInfo, newerThan time.Time) map[string]any {
	settings := make(map[string]any)
	for line := range reverseThreadRolloutRecords(info) {
		var record struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Payload   struct {
				Type     string  `json:"type"`
				Model    string  `json:"model"`
				Effort   *string `json:"effort"`
				Settings *struct {
					Model  string  `json:"model"`
					Effort *string `json:"reasoning_effort"`
					Tier   *string `json:"service_tier"`
				} `json:"thread_settings"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if !newerThan.IsZero() && !record.Timestamp.After(newerThan) {
			continue
		}
		p := record.Payload
		if record.Type == "event_msg" && p.Type == "thread_settings_applied" && p.Settings != nil && p.Settings.Model != "" {
			p.Model, p.Effort = p.Settings.Model, p.Settings.Effort
			if _, known := settings["service_tier"]; !known {
				settings["service_tier"] = nil
				if p.Settings.Tier != nil {
					settings["service_tier"] = *p.Settings.Tier
				}
			}
		} else if record.Type != "turn_context" || p.Model == "" {
			continue
		}
		if _, known := settings["model"]; !known {
			settings["model"] = p.Model
			settings["model_reasoning_effort"] = nil
			if p.Effort != nil {
				settings["model_reasoning_effort"] = *p.Effort
			}
		}
		if len(settings) == 3 {
			break
		}
	}
	return settings
}
