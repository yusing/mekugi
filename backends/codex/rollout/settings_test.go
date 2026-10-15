package rollout

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func resumeSettingsRollout(t *testing.T, records ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := `{"type":"session_meta","payload":{"id":"saved"}}` + "\n" + strings.Join(records, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResumeSettings(t *testing.T) {
	initial := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"first","reasoning_effort":"low","service_tier":"priority"}}}`
	for _, tc := range []struct {
		name    string
		records []string
		want    map[string]any
	}{
		{"idle changes win", []string{initial, `{"type":"turn_context","payload":{"model":"turn","effort":"high"}}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"idle","reasoning_effort":"medium","service_tier":"flex"}}}`, ""}, map[string]any{"model": "idle", "model_reasoning_effort": "medium", "service_tier": "flex"}},
		{"turn preserves last tier", []string{initial, `{"type":"turn_context","payload":{"model":"turn","effort":"high"}}`, ""}, map[string]any{"model": "turn", "model_reasoning_effort": "high", "service_tier": "priority"}},
		{"cleared defaults", []string{initial, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"idle"}}}`, ""}, map[string]any{"model": "idle", "model_reasoning_effort": nil, "service_tier": nil}},
		{"older history", []string{`{"type":"turn_context","payload":{"model":"legacy","effort":"low"}}`, ""}, map[string]any{"model": "legacy", "model_reasoning_effort": "low"}},
		{"partial and invalid records ignored", []string{initial, `{broken}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"partial","service_tier":"flex"}}}`}, map[string]any{"model": "first", "model_reasoning_effort": "low", "service_tier": "priority"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResumeSettings("saved", resumeSettingsRollout(t, tc.records...), time.Time{}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("saved settings: got %#v, want %#v", got, tc.want)
			}
		})
	}
	path := resumeSettingsRollout(t, initial, "")
	if got := ResumeSettings("other", path, time.Time{}); len(got) != 0 {
		t.Fatalf("borrowed another thread's settings: %#v", got)
	}
}

func TestResumeSettingsBound(t *testing.T) {
	path := resumeSettingsRollout(t, `{"type":"turn_context","payload":{"model":"too-old","effort":"high"}}`, "")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Seek(9<<20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", path, time.Time{}); len(got) != 0 {
		t.Fatalf("read outside retention bound: %#v", got)
	}
	if _, err := f.WriteString(`{"type":"turn_context","payload":{"model":"retained","effort":"low"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", path, time.Time{}); got["model"] != "retained" {
		t.Fatalf("lost retained settings: %#v", got)
	}
}
