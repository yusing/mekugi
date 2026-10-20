package presentation

import (
	"strings"
	"testing"
)

func TestFoldStagesIntoTheirCommit(t *testing.T) {
	stage := Value{Kind: "op", Verb: "Stage", Label: "`a.go` · git"}
	commit := Value{Kind: "op", Verb: "Commit", Label: "`x` · git"}
	check := Value{Kind: "op", Verb: "Check", Label: "`working tree` · git diff --check"}
	verbs := func(blocks []Value) string {
		var out []string
		for _, block := range blocks {
			out = append(out, block.Verb)
		}
		return strings.Join(out, " ")
	}
	failed, skipped := stage, commit
	failed.ExitCode, skipped.Skipped = 1, true
	for _, tc := range []struct {
		blocks []Value
		want   string
	}{
		{[]Value{check, stage, stage, commit}, "Check Commit"},
		{[]Value{stage}, "Stage"},
		{[]Value{stage, check, commit}, "Stage Check Commit"},
		{[]Value{failed, skipped}, "Stage Commit"},
		{[]Value{stage, skipped}, "Stage Commit"},
	} {
		if got := verbs(FoldStages(tc.blocks)); got != tc.want {
			t.Errorf("%s: folded to %q, want %q", verbs(tc.blocks), got, tc.want)
		}
	}
}
