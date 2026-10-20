package rollout

import (
	"reflect"
	"testing"
	"time"
)

func TestContextUsageKnownZeroAndMissingEvidence(t *testing.T) {
	zero := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":0},"model_context_window":200000}}}`
	for _, tc := range []struct {
		name   string
		thread string
		record string
		want   ContextUsage
		known  bool
	}{
		{"known zero", "saved", zero, ContextUsage{Window: 200000}, true},
		{"absent", "saved", "", ContextUsage{}, false},
		{"null info", "saved", `{"type":"event_msg","payload":{"type":"token_count","info":null}}`, ContextUsage{}, false},
		{"null last usage", "saved", `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":null,"model_context_window":200000}}}`, ContextUsage{}, false},
		{"mismatched identity", "other", zero, ContextUsage{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := resumeSettingsRollout(t, tc.record, "")
			got, known := ReadContextUsage(tc.thread, path)
			if got != tc.want || known != tc.known {
				t.Fatalf("context: got %+v, known=%t; want %+v, known=%t", got, known, tc.want, tc.known)
			}
		})
	}
}

func TestResumeSettingsTimestampCutoffAndNullablePrecedence(t *testing.T) {
	path := resumeSettingsRollout(t,
		`{"timestamp":"2026-01-01T00:00:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"old","reasoning_effort":"high","service_tier":"priority"}}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"cleared","reasoning_effort":null,"service_tier":null}}}`,
		`{"timestamp":"2026-01-01T00:00:02Z","type":"turn_context","payload":{"model":"latest-turn","effort":null}}`,
		// Later file positions cannot override eligible evidence with stale or undated settings.
		`{"timestamp":"2026-01-01T00:00:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"stale","reasoning_effort":"low","service_tier":"flex"}}}`,
		`{"type":"turn_context","payload":{"model":"undated","effort":"high"}}`,
		"",
	)
	for _, tc := range []struct {
		name   string
		second int
		want   map[string]any
	}{
		{"newer turn preserves cleared defaults", 0, map[string]any{"model": "latest-turn", "model_reasoning_effort": nil, "service_tier": nil}},
		{"equal timestamp excluded from tier fallback", 1, map[string]any{"model": "latest-turn", "model_reasoning_effort": nil}},
		{"nothing strictly newer", 2, map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cutoff := time.Date(2026, time.January, 1, 0, 0, tc.second, 0, time.UTC)
			if got := ResumeSettings("saved", path, cutoff); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("settings after %s: got %#v, want %#v", cutoff, got, tc.want)
			}
		})
	}
}
