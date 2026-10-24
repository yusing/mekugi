package terminalsession

import (
	"strings"
	"testing"
)

func BenchmarkShellComposeStyledPanes(b *testing.B) {
	s := Shell{DiffOpen: true, AuxiliaryLabel: "Changes", RosterLabel: "Agents"}
	layout, left, right, framed, roster := s.Panes.Geometry(172, 44, 3)
	plan := ShellPlan{layout, left, right, 172, 44, roster, framed}
	body := make([]string, 38)
	for i := range body {
		body[i] = "\x1b[32m+\x1b[0m " + strings.Repeat("const value = 42; ", 4)
	}
	in := ShellContent{Main: body, Diff: body, MainName: "Main", DiffSummary: "+123 -45"}
	b.ReportAllocs()
	for b.Loop() {
		s.Compose(plan, in)
	}
}
