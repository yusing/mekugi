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
	if got != "Started · `gpt-effective` `high` `fast`" || strings.Contains(got, "Private assignment body") {
		t.Fatalf("native start header = %q", got)
	}
}
