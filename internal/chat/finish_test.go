package chat

import "testing"

func TestFinishEvidence(t *testing.T) {
	for _, test := range []struct {
		reason             FinishReason
		status, incomplete string
	}{
		{Stop, "completed", ""},
		{ToolCalls, "completed", ""},
		{Length, "incomplete", "max_output_tokens"},
		{ContentFilter, "incomplete", "content_filter"},
		{"", "", ""},
		{"future", "", ""},
	} {
		if test.reason.ResponseStatus() != test.status || test.reason.IncompleteReason() != test.incomplete {
			t.Fatalf("incorrect finish evidence: %q", test.reason)
		}
	}
}
