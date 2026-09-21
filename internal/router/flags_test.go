package router

import (
	"slices"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, prefix := range [][]string{
		nil,
		{"--debug"},
		{"--debug", "--capture-output", "capture.jsonl"},
		{"--main-mentor-handoff", "--mentor-handoff=false"},
		{"--grok"},
		{"--grok=false", "--timeout", "30s"},
		{"--capture-output", "wrap", "--grok"},
		{"--capture-output", "--"},
	} {
		command := []string{"codex", "exec", "--model", "example", "--", "wrap"}
		args := append(slices.Clone(prefix), command...)
		gotPrefix, gotCommand, err := SplitCommand(args)
		if err != nil || !slices.Equal(gotPrefix, prefix) || !slices.Equal(gotCommand, command) {
			t.Errorf("SplitCommand(%q) = %q, %q, %v", args, gotPrefix, gotCommand, err)
		}
	}
}

func TestSplitCommandRestrictionsAndDelimiter(t *testing.T) {
	for _, args := range [][]string{
		{"--listen", "127.0.0.1:8080", "codex"},
		{"--provider-base-url=https://example.com", "codex"},
		{"--unknown", "codex"},
	} {
		if _, _, err := SplitCommand(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	prefix, command, err := SplitCommand([]string{"--grok", "--", "codex"})
	if err != nil || !slices.Equal(prefix, []string{"--grok", "--"}) || !slices.Equal(command, []string{"codex"}) {
		t.Fatalf("delimiter = %q, %q, %v", prefix, command, err)
	}
}
