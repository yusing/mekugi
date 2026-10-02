package router

import (
	"slices"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, prefix := range [][]string{
		nil,
		{"--debug"},
		{"--ansi-faint=off"},
		{"--ansi-faint", "on"},
		{"--ansi-faint=auto"},
		{"--debug", "--capture-output", "capture.jsonl"},
		{"--grok-auth-file", "auth.json"},
		{"--timeout", "30s"},
		{"--capture-output", "wrap"},
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
		{"--grok", "codex"},
		{"--grok=false", "codex"},
		{"--timeout", "not-a-duration", "grok"},
	} {
		if _, _, err := SplitCommand(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	prefix, command, err := SplitCommand([]string{"--debug", "--", "grok"})
	if err != nil || !slices.Equal(prefix, []string{"--debug", "--"}) || !slices.Equal(command, []string{"grok"}) {
		t.Fatalf("delimiter = %q, %q, %v", prefix, command, err)
	}
}

func TestSplitCommandStandalone(t *testing.T) {
	for _, args := range [][]string{nil, {"--yolo", "-m", "grok:grok-4.7"}, {"exec", "--yolo", "hello"}, {"resume", "--last"}} {
		prefix, command, err := SplitCommand(append([]string{"--debug"}, args...))
		if err != nil || !slices.Equal(prefix, []string{"--debug"}) || !slices.Equal(command, args) {
			t.Fatalf("standalone split = %q, %q, %v", prefix, command, err)
		}
	}
}

func TestHasModelOverridePreservesOperands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-m", "grok-4.5"}, true},
		{[]string{"--model=grok-4.5"}, true},
		{[]string{"-mgrok-4.5"}, true},
		{[]string{"-c", `"model"="grok-4.5"`}, true},
		{[]string{"--config=model='grok-4.5'"}, true},
		{[]string{"-cmodel='grok-4.5'"}, true},
		{[]string{"--image", "-model.png"}, false},
		{[]string{"--output-schema", "-model.json"}, false},
		{[]string{"-o", "-model-output.txt"}, false},
		{[]string{"--thread-source", "-model-source"}, false},
		{[]string{"-c", "other=-model"}, false},
		{[]string{"--profile", "-model-profile", "--", "-m", "literal"}, false},
	} {
		if got := HasModelOverride(tc.args); got != tc.want {
			t.Fatalf("HasModelOverride(%q) = %t, want %t", tc.args, got, tc.want)
		}
	}
}
