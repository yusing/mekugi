package sessioncontrol

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"testing"
)

func claudeGenerationFixture(t *testing.T) []jsontext.Value {
	t.Helper()
	// Captured from the official SDK by worker.test.mjs's gated native CLI test.
	data, err := os.ReadFile("testdata/claude-tool-generation.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var events []jsontext.Value
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		events = append(events, line)
	}
	return events
}

func TestClaudeToolGenerationCapturedStream(t *testing.T) {
	runtime, _, _ := claudeFixture(t, nil)
	want := []string{"", "Spawning subagent", "Spawning subagent `worker`", "Spawning subagent `worker`", "", "", ""}
	for i, raw := range claudeGenerationFixture(t) {
		if _, err := runtime.HandleEvent(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
		facts, _ := runtime.Metrics()
		if facts.Generating != want[i] {
			t.Fatalf("native SDK event %d: label=%q, want %q", i, facts.Generating, want[i])
		}
	}
}

func TestClaudeToolGenerationAdmissionAndTransportLoss(t *testing.T) {
	runtime, selected, settings := claudeFixture(t, nil)
	events := claudeGenerationFixture(t)
	for _, raw := range events[:3] {
		if _, err := runtime.HandleEvent(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"session_id", "parent_tool_use_id"} {
		var event map[string]jsontext.Value
		if err := json.Unmarshal(events[4], &event); err != nil {
			t.Fatal(err)
		}
		event[field] = jsontext.Value(`"another-native-source"`)
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.HandleEvent(t.Context(), raw)
		if field == "session_id" && err == nil {
			t.Fatal("unrelated native session was admitted")
		}
		facts, _ := runtime.Metrics()
		if facts.Generating != "Spawning subagent `worker`" {
			t.Fatalf("%s observation changed primary generation: %+v", field, facts)
		}
	}
	runtime.TransportClosed(errors.New("fixture disconnect"))
	facts, _ := runtime.Metrics()
	if facts.Generating != "" {
		t.Fatal("transport loss retained native tool generation")
	}
	info, err := runtime.Thread(t.Context(), "child")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewClaudeRuntime(t.Context(), selected, info, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	facts, _ = restored.Metrics()
	if facts.Generating != "" {
		t.Fatal("restoration revived transient generation")
	}
}
