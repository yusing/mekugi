package chat

import "testing"

func TestFinishEvidence(t *testing.T) {
	for _, test := range []struct {
		reason FinishReason
		status string
	}{
		{Stop, "completed"},
		{ToolCalls, "completed"},
		{Length, "incomplete"},
		{ContentFilter, "incomplete"},
		{"", ""},
		{"future", ""},
	} {
		if test.reason.ResponseStatus() != test.status {
			t.Fatalf("incorrect finish evidence: %q", test.reason)
		}
	}
}
