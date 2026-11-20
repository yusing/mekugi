package terminalsession

import (
	"strings"
	"testing"
)

func BenchmarkShellComposeStyledPanes(b *testing.B) {
	s := Shell{DiffOpen: true, AuxiliaryLabel: "Changes", RosterLabel: "Agents"}
	plan := s.Plan(172, 44)
	body := make([]string, 38)
	for i := range body {
		body[i] = "\x1b[32m+\x1b[0m " + strings.Repeat("const value = 42; ", 4)
	}
	in := ShellContent{Main: body, Diff: body}
	b.ReportAllocs()
	for b.Loop() {
		s.Compose(plan, in)
	}
}
