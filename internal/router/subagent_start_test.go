package router

import (
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestSubagentStartHasOnlyObservedModelHeader(t *testing.T) {
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-effective", "reasoning": map[string]any{"effort": "high"}, "service_tier": "priority",
		"input": []any{nativeAssignmentTestMessage("/root/child", "NEW_TASK", "Private assignment body")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := nativeSubagentStart(&request)
	if got == nil || *got != (activityStart{model: "gpt-effective", effort: "high", tier: "fast"}) || strings.Contains(got.label(), "Private assignment body") {
		t.Fatalf("native start = %+v", got)
	}
}
