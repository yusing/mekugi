package main

import (
	"slices"
	"testing"
)

func TestReasoningShortcuts(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		got := expandReasoningShortcuts([]string{"--yolo", "--" + effort})
		want := []string{"--yolo", "-c", `model_reasoning_effort="` + effort + `"`}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: got %q, want %q", effort, got, want)
		}
		if _, _, _, err := appServerArgs(got); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"--", "--high"}, {"-m", "--high"}, {"--config", "--low"}, {"--image", "--max"}, {"mcp", "add", "example", "executable", "--high"}, {"sandbox", "linux", "executable", "--low"}} {
		if got := expandReasoningShortcuts(args); !slices.Equal(got, args) {
			t.Fatalf("operands changed: %q -> %q", args, got)
		}
	}
}
