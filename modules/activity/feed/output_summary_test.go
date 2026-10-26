package feed

import (
	"strings"
	"testing"
)

func TestSummaryTailReportsClippedBytes(t *testing.T) {
	var tail SummaryTail
	tail.Write(strings.Repeat("界", SummaryLineBytes))
	if lines, omitted, clipped := tail.Lines(); !clipped || omitted != 0 || len(lines) != 1 {
		t.Fatalf("single clipped line: %q omitted=%d clipped=%t", lines, omitted, clipped)
	}
	tail.Write("\n" + strings.Repeat("complete…\n", SummaryLines))
	if lines, omitted, clipped := tail.Lines(); clipped || omitted != 1 || len(lines) != SummaryLines {
		t.Fatalf("clipped line left summary: %q omitted=%d clipped=%t", lines, omitted, clipped)
	}
}
