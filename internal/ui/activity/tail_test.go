package activity

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOutputTailStreamsBoundedTail(t *testing.T) {
	output := "build\r\n\r\n10%\r50%\r100%\ndone\x1b[2K\n" + strings.Repeat("x", 3*OutputTailBytes) + "\nnext"
	whole, wholeOmit := TailOutput(output)
	var chunked OutputTail
	for chunk := range slices.Values(strings.SplitAfter(output, "")) {
		chunked.Write(chunk) // One byte at a time splits CRLF and escapes.
	}
	tail, omitted := chunked.Lines()
	if !reflect.DeepEqual(tail, whole) || omitted != wholeOmit {
		t.Fatalf("chunked tail = %q, %d; whole = %q, %d", tail, omitted, whole, wholeOmit)
	}
	want := []string{"build", "100%", "done", strings.Repeat("x", OutputTailBytes) + "…", "next"}
	if !reflect.DeepEqual(tail, want) || omitted != 0 {
		t.Fatalf("tail = %q omitted %d, want %q", tail, omitted, want)
	}
	var long OutputTail
	for i := range 10000 {
		long.Write(strings.Repeat("y", 100) + fmt.Sprintf(" %d\n", i))
	}
	if len(long.lines) > OutputTailLines || len(long.line) > OutputTailBytes+1 {
		t.Fatalf("tail retained %d lines and a %d-byte line", len(long.lines), len(long.line))
	}
	if tail, omitted := long.Lines(); len(tail) != OutputTailLines || omitted != 10000-OutputTailLines || tail[4] != strings.Repeat("y", 100)+" 9999" {
		t.Fatalf("long tail = %q omitted %d", tail, omitted)
	}
}

func TestTailRowsSkipsGaps(t *testing.T) {
	rows := []string{"one", "", "two", "│", "three", "", "four"}
	if tail, hidden := TailRows(rows, 3); !reflect.DeepEqual(tail, []string{"two", "three", "four"}) || hidden != 1 {
		t.Fatalf("tail = %q hidden %d", tail, hidden)
	}
	if tail, hidden := TailRows(rows[:1], 3); !reflect.DeepEqual(tail, []string{"one"}) || hidden != 0 {
		t.Fatalf("short tail = %q hidden %d", tail, hidden)
	}
	if MoreLines(1) != "+1 line" || MoreLines(4) != "+4 lines" {
		t.Fatal("line counts")
	}
}
