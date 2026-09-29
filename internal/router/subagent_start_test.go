package router

import (
	"strings"
	"testing"
)

func TestSubagentStartHasOnlyObservedModelHeader(t *testing.T) {
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-effective", "reasoning": map[string]any{"effort": "high"}, "service_tier": "priority",
		"input": []any{journalTestAssignment("/root/child", "NEW_TASK", "Private assignment body")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := nativeSubagentStart(&request)
	if got == nil || *got != (activityStart{model: "gpt-effective", effort: "high", tier: "fast"}) || strings.Contains(got.label(), "Private assignment body") {
		t.Fatalf("native start = %+v", got)
	}
}
