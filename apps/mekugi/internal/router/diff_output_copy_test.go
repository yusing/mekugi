package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/terminalui"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestDiffOutputSectionSourceSelection(t *testing.T) {
	source := "--- /dev/null\n+++ \"a new.go\"\n@@ -0,0 +1,2 @@\n+package main\n+var answer = 42"
	p := activityui.Painter{CopySource: true}
	block := activityui.Block{Kind: "op", Verb: "Diff", Tail: strings.Split(source, "\n")}
	raw := p.Block(block, 40)[1:]
	rows := make([]string, len(raw))
	meta := make([][]terminalui.CopySpan, len(raw))
	for i, row := range raw {
		rows[i], meta[i] = terminalui.ExtractCopy(row)
	}
	u := selectionTestUI(rows...)
	u.frame.Copy = append(meta, nil)
	selectionTestDrag(t, u, 0, 0, 39, len(rows)-1)
	if got := u.selection.Text(); got != source {
		t.Fatalf("transcript copied=%q", got)
	}

	u, _ = outputSelectionFixture(source, false)
	_, heading := outputSelectionPoint(t, u, "A a new.go")
	_, last := outputSelectionPoint(t, u, "+var answer")
	outputSelectionDrag(u, u.output.rect.X+2, heading-1, u.output.rect.X+u.output.rect.W-3, last)
	if got := u.selection.Text(); got != source {
		t.Fatalf("dialog copied=%q", got)
	}
}
