package launch

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

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
	got := observedSubagentStart(t, &request)
	if got == nil || *got != (binding.SubagentSettings{Model: "gpt-effective", Effort: "high", Tier: "fast"}) || strings.Contains(testActivityStart(t, &request).Label(), "Private assignment body") {
		t.Fatalf("native start = %+v", got)
	}
}

func testActivityStart(t *testing.T, request *codexresponses.Request) *sessionpresentation.ActivityStart {
	observed := observedSubagentStart(t, request)
	if observed == nil {
		return nil
	}
	return &sessionpresentation.ActivityStart{Model: observed.Model, Effort: observed.Effort, Tier: observed.Tier}
}
