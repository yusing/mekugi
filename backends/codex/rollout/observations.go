package rollout

import (
	"context"
	json "encoding/json/v2"
	"io"
	"path/filepath"
	"strings"
)

// Observations contains retained facts, not execution authority or inferred defaults.
type Observations struct {
	Model, Effort, Tier   *string
	ContextTokens, Window *uint64
	Compactions           *uint64
}

// ReadObservations reads admitted history on demand without restoring a runtime.
// The bounded full read is needed to distinguish a complete compaction count
// from a count over only the most recent tail.
func ReadObservations(ctx context.Context, thread, path string) Observations {
	if !filepath.IsAbs(path) {
		return Observations{}
	}
	var result Observations
	var owner string
	completed := make(map[string]struct{})
	err := withRetainedSource(thread, path, func(source io.Reader) error {
		return scanReplayLines(ctx, path, source, func(line []byte) error {
			var record struct {
				Type    string `json:"type"`
				Payload struct {
					ID       string  `json:"id"`
					Type     string  `json:"type"`
					Model    string  `json:"model"`
					Thread   string  `json:"thread_id"`
					Effort   *string `json:"effort"`
					Settings *struct {
						Model  *string `json:"model"`
						Effort *string `json:"reasoning_effort"`
						Tier   *string `json:"service_tier"`
					} `json:"thread_settings"`
					Info *struct {
						Last *struct {
							Total *uint64 `json:"total_tokens"`
						} `json:"last_token_usage"`
						Window uint64 `json:"model_context_window"`
					} `json:"info"`
					Item struct {
						ID     string `json:"id"`
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"item"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(line, &record); err != nil {
				return err
			}
			p := record.Payload
			if record.Type == "session_meta" {
				owner = p.ID
				return nil
			}
			if record.Type == "event_msg" && p.Type == "thread_settings_applied" && p.Thread == thread && p.Settings != nil {
				owner = thread
				result.Model, result.Effort, result.Tier = p.Settings.Model, p.Settings.Effort, p.Settings.Tier
			}
			if owner != thread || (p.Thread != "" && p.Thread != thread) {
				return nil
			}
			if record.Type == "turn_context" && p.Model != "" {
				result.Model, result.Effort = new(p.Model), p.Effort
			}
			if record.Type != "event_msg" {
				return nil
			}
			if p.Type == "token_count" && p.Info != nil && p.Info.Last != nil && p.Info.Last.Total != nil {
				result.ContextTokens = p.Info.Last.Total
				result.Window = nil
				if p.Info.Window > 0 {
					result.Window = new(p.Info.Window)
				}
			}
			if p.Type == "item_completed" && strings.EqualFold(p.Item.Type, "contextCompaction") && p.Item.ID != "" && p.Item.Status != "failed" && p.Item.Status != "interrupted" && p.Item.Status != "inProgress" {
				completed[p.Item.ID] = struct{}{}
			}
			return nil
		})
	})
	if err != nil {
		return Observations{}
	}
	result.Compactions = new(uint64(len(completed)))
	return result
}
