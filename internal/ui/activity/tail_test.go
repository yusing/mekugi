package activity

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOutputTailStreamsBoundedTail(t *testing.T) {
	output := "build\r\n\r\n10%\r50%\r100%\ndone\x1b[2K\n" + strings.Repeat("x", 3*OutputTailBytes) + "\nnext"
	whole, wholeOmit := TailOutput(output)
	var chunked OutputTail
	for chunk := range slices.Values(strings.SplitAfter(output, "")) {
		chunked.Write(chunk) // One byte at a time splits CRLF and escapes.
	}
	chunked.Reveal(chunked.Pending())
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
	if len(long.lines) > OutputTailLines || len(long.pending) > OutputPendingLines || len(long.line) > OutputTailBytes+1 {
		t.Fatalf("tail retained %d lines, %d pending and a %d-byte line", len(long.lines), len(long.pending), len(long.line))
	}
	long.Reveal(long.Pending())
	if tail, omitted := long.Lines(); len(tail) != OutputTailLines || omitted != 10000-OutputTailLines || tail[4] != strings.Repeat("y", 100)+" 9999" {
		t.Fatalf("long tail = %q omitted %d", tail, omitted)
	}
}

func TestOutputTailRollsThroughBurst(t *testing.T) {
	var tail OutputTail
	for i := range 20 {
		tail.Write(fmt.Sprintf("line %d\n", i))
	}
	tail.Write("partial")
	if lines, _ := tail.Lines(); len(lines) != 0 {
		t.Fatalf("burst showed before rolling: %q", lines)
	}
	var shown []string
	frames := 0
	for tail.Roll() {
		frames++
		lines, _ := tail.Lines()
		if last := lines[len(lines)-1]; len(shown) == 0 || shown[len(shown)-1] != last {
			shown = append(shown, last)
		}
	}
	if frames < 5 || len(shown) < 5 || shown[0] == "line 19" {
		t.Fatalf("burst jumped to its tail: %d frames showing %q", frames, shown)
	}
	if lines, omitted := tail.Lines(); lines[len(lines)-1] != "partial" || omitted != 16 {
		t.Fatalf("rolled tail = %q omitted %d, want the unfinished line last", lines, omitted)
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

func TestOutputRowsCountEarlierLinesInVerbColumn(t *testing.T) {
	var p Painter
	block := Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "a.go"}}, Tail: []string{"one", "two", "three", "four", "five"}, TailOmitted: 7}
	plain := func(block Block) string {
		var rows []string
		for _, row := range p.Block(block, 60) {
			rows = append(rows, ansi.Strip(row))
		}
		return strings.Join(rows, "\n")
	}
	if got, want := plain(block), "Read   a.go\n+7     ┆ one\n       ┆ two\n       ┆ three\n       ┆ four\n       ┆ five"; got != want {
		t.Fatalf("open output =\n%s\nwant\n%s", got, want)
	}
	block.TailRows = 3
	if got, want := plain(block), "Read   a.go\n+9     ┆ three\n       ┆ four\n       ┆ five"; got != want {
		t.Fatalf("compact output =\n%s\nwant\n%s", got, want)
	}
	block.Collapsed = true
	if got, want := plain(block), "Read   a.go\n       ┆ … +12 lines"; got != want {
		t.Fatalf("collapsed output =\n%s\nwant\n%s", got, want)
	}
	// A count wider than the verb column keeps its own row.
	block.Collapsed, block.TailRows, block.TailOmitted = false, 0, 1234567
	if got, want := plain(block), "Read   a.go\n       ┆ … 1234567 earlier lines\n       ┆ one"; !strings.HasPrefix(got, want) {
		t.Fatalf("wide count output =\n%s\nwant prefix\n%s", got, want)
	}
}
