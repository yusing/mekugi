package router

import (
	"strings"
	"testing"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestDiffOutputSectionSourceSelection(t *testing.T) {
	source := "--- /dev/null\n+++ \"a new.go\"\n@@ -0,0 +1,2 @@\n+package main\n+var answer = 42"
	p := activityui.Painter{CopySource: true}
	block := activityui.Block{Kind: "op", Verb: "Diff", Tail: strings.Split(source, "\n")}
	raw := p.Block(block, 40)[1:]
	rows := make([]string, len(raw))
	meta := make([][]activityui.CopySpan, len(raw))
	for i, row := range raw {
		rows[i], meta[i] = activityui.ExtractCopy(row)
	}
	u := selectionTestUI(rows...)
	u.paintedCopy = append(meta, nil)
	selectionTestDrag(t, u, 0, 0, 39, len(rows)-1)
	if got := u.selection.text(); got != source {
		t.Fatalf("transcript copied=%q", got)
	}

	u, _ = outputSelectionFixture(source, false)
	_, heading := outputSelectionPoint(t, u, "A a new.go")
	_, last := outputSelectionPoint(t, u, "+var answer")
	outputSelectionDrag(u, u.output.rect.x+2, heading-1, u.output.rect.x+u.output.rect.w-3, last)
	if got := u.selection.text(); got != source {
		t.Fatalf("dialog copied=%q", got)
	}
}
