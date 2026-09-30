package activity

import (
	"slices"
	"strings"
	"testing"
)

func TestOutputConsumersSplitCRLFAndOverwrite(t *testing.T) {
	var retained Output
	var tail OutputTail
	for _, chunk := range []string{"\r", "\n\r", "\nA\r", "\n10%\r", "100%\r", "\nlast"} {
		retained.Write(chunk)
		tail.Write(chunk)
	}
	if got := retained.View().Lines; !slices.Equal(got, []string{"", "", "A", "100%", "last"}) {
		t.Fatalf("retained lines = %q", got)
	}
	if tail.Pending() != 2 {
		t.Fatalf("tail pending = %d, want two nonblank complete lines", tail.Pending())
	}
	if got, _ := tail.Lines(); len(got) != 0 {
		t.Fatalf("tail bypassed reveal pacing: %q", got)
	}
	tail.Flush()
	if got, omitted := tail.Lines(); !slices.Equal(got, []string{"A", "100%", "last"}) || omitted != 2 {
		t.Fatalf("tail lines = %q, omitted %d; want two complete blank lines accounted for", got, omitted)
	}
}

func TestOutputConsumersKeepDistinctLineLimits(t *testing.T) {
	var retained Output
	var tail OutputTail
	long := strings.Repeat("x", OutputLineBytes+4)
	for _, chunk := range []string{long[:OutputTailBytes+1], long[OutputTailBytes+1:], "\r", "\nend\n"} {
		retained.Write(chunk)
		tail.Write(chunk)
	}
	view := retained.View()
	if !view.Truncated || view.Dropped != 0 || !slices.Equal(view.Lines, []string{strings.Repeat("x", OutputLineBytes) + "…", "end"}) {
		t.Fatalf("retained lines have incorrect limit/reset: lengths %v, truncated %v, dropped %d", lineLengths(view.Lines), view.Truncated, view.Dropped)
	}
	if tail.Pending() != 2 {
		t.Fatalf("tail pending = %d, want two complete lines", tail.Pending())
	}
	tail.Flush()
	if got, omitted := tail.Lines(); !slices.Equal(got, []string{strings.Repeat("x", OutputTailBytes) + "…", "end"}) || omitted != 0 {
		t.Fatalf("tail lines have incorrect limit/reset: lengths %v, omitted %d", lineLengths(got), omitted)
	}
}

func lineLengths(lines []string) []int {
	lengths := make([]int, len(lines))
	for i, line := range lines {
		lengths[i] = len(line)
	}
	return lengths
}
