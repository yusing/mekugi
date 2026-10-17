package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

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
	if got == nil || *got != (binding.SubagentSettings{Model: "gpt-effective", Effort: "high", Tier: "fast"}) || strings.Contains(testActivityStart(&request).label(), "Private assignment body") {
		t.Fatalf("native start = %+v", got)
	}
}

func testActivityStart(request *codexresponses.Request) *activityStart {
	observed := nativeSubagentStart(request)
	if observed == nil {
		return nil
	}
	return &activityStart{model: observed.Model, effort: observed.Effort, tier: observed.Tier}
}
