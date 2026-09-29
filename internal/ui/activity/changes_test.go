package activity

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestChangeRowsBoundRowsAndWidth(t *testing.T) {
	var rows []ChangeRow
	for i := range ChangeRowsShown + 2 {
		rows = append(rows, ChangeRow{Verb: "Edited", Label: fmt.Sprintf("internal/some/deeply/nested/package/file%d.go", i), Added: i + 1, Removed: 1, Note: "a long retained reason"})
	}
	var p Painter
	block := Block{Kind: "op", Verb: "Run", Code: "mchanges --summary", Tail: []string{"x", "y"}, Changes: rows}
	lines := p.Block(block, 50)
	if got := len(lines); got != 1+ChangeRowsShown+1 {
		t.Fatalf("rows = %d, want %d", got, 1+ChangeRowsShown+1)
	}
	if last := ansi.Strip(lines[len(lines)-1]); strings.TrimSpace(last) != "… +2 more" {
		t.Fatalf("last row = %q", last)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 50 {
			t.Fatalf("row exceeds width: %q", ansi.Strip(line))
		}
	}
	// Collapsed, the rows give way to the output's line count.
	block.Collapsed = true
	if got := ansi.Strip(strings.Join(p.Block(block, 50), "\n")); strings.Contains(got, "Edited") || !strings.Contains(got, "┆ … +2 lines") {
		t.Fatalf("collapsed change rows =\n%s", got)
	}
}
