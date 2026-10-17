package capture

import (
	"slices"
	"testing"
)

func TestCaptureCompletionEvidence(t *testing.T) {
	unknown := Completion{CodeMode: true, Succeeded: true, Exit: new(0)}
	failed := Completion{Exit: new(7)}
	for _, test := range []struct {
		name    string
		members []Completion
		status  string
		exit    *int
		failed  []bool
	}{
		{"ordinary failure", []Completion{failed}, StatusFailed, new(7), []bool{true}},
		{"outer success", []Completion{unknown}, "", new(0), []bool{false}},
		{"confirmed success", []Completion{{CodeMode: true, Confirmed: true, Commands: []CommandOutcome{{Completed: true, Exit: new(0)}}}}, StatusCompleted, new(0), []bool{false}},
		{"nested failure", []Completion{{CodeMode: true, Succeeded: true, Exit: new(0), Confirmed: true, Commands: []CommandOutcome{{Completed: true, Exit: new(7)}}}}, StatusFailed, new(7), []bool{true}},
		{"unknown nested exit", []Completion{{CodeMode: true, Succeeded: true, Confirmed: true, Commands: []CommandOutcome{{Completed: true}}}}, StatusFailed, nil, []bool{true}},
		{"unknown then failure", []Completion{unknown, failed}, StatusFailed, nil, []bool{false, true}},
		{"failure then unknown", []Completion{failed, unknown}, StatusFailed, nil, []bool{true, false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome := Outcome{Coverage: CoveragePartial, Scope: []string{"source.txt"}}
			flags := outcome.ApplyCompletions(test.members)
			if outcome.Status != test.status || (outcome.Exit == nil) != (test.exit == nil) || outcome.Exit != nil && *outcome.Exit != *test.exit || !slices.Equal(flags, test.failed) || outcome.Coverage != CoveragePartial || !slices.Equal(outcome.Scope, []string{"source.txt"}) {
				t.Fatalf("completion changed outcome or capture facts: %+v flags=%v", outcome, flags)
			}
		})
	}
}
