package run

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandargs"
)

func TestMRunCombinedCaptureMatchesBoundedRows(t *testing.T) {
	for _, tail := range []bool{false, true} {
		for _, source := range []string{strings.Repeat("x", 132), strings.Repeat("x", 133), strings.Repeat("界\n", 100), strings.Repeat("x", 200) + "\nlast", "a\xff\nb\n", strings.Repeat("row\n", 2000)} {
			for _, limit := range []int{2, 1000} {
				capture := mrunCapture{buffer: make([]byte, 132), maxLines: limit, tail: tail}
				for i := range len(source) {
					capture.Write([]byte(source[i : i+1]))
					retained := 0
					for _, row := range capture.rows[capture.rowStart:] {
						retained += len(row)
					}
					if retained > len(capture.buffer) {
						t.Fatalf("completed rows retained %d bytes", retained)
					}
				}
				rows := strings.SplitAfter(source, "\n")
				if rows[len(rows)-1] == "" {
					rows = rows[:len(rows)-1]
				}
				omitted := len(rows) > limit
				if omitted {
					if tail {
						rows = rows[len(rows)-limit:]
					} else {
						rows = rows[:limit]
					}
				}
				window := mrunCapture{buffer: make([]byte, 132), tail: tail, omitted: omitted}
				for _, row := range rows {
					line := mrunCapture{buffer: make([]byte, 132), tail: tail}
					line.Write([]byte(row))
					window.Write([]byte(line.text()))
					window.omitted = window.omitted || line.omitted
				}
				if got, want := capture.text(), window.text(); got != want || capture.omitted != window.omitted {
					t.Fatalf("tail=%t lines=%d: got %q omitted=%t, want %q omitted=%t", tail, limit, got, capture.omitted, want, window.omitted)
				}
			}
		}
	}
}

func BenchmarkCombinedCapture(b *testing.B) {
	row := []byte(strings.Repeat("x", 16<<10) + "\n")
	for _, tail := range []bool{false, true} {
		b.Run(fmt.Sprint(tail), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				capture := mrunCapture{buffer: make([]byte, 132), maxLines: 512, tail: tail}
				for range 1024 {
					capture.Write(row)
				}
				if len(capture.text()) > 132 || !capture.omitted {
					b.Fatal("capture window changed")
				}
			}
		})
	}
}

func TestMRunCapture(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		for _, chunks := range [][]string{
			{}, {""}, {"abc"}, {"12345678"}, {"123456789012"},
			{"abc", "def", "ghi", "jkl", "mn"}, {"123456789012", "x", "yz"},
			{"1234567", "ab", "12345678", "z"},
		} {
			capture := mrunCapture{buffer: make([]byte, 8), tail: tail}
			for _, chunk := range chunks {
				n, err := capture.Write([]byte(chunk))
				if err != nil || n != len(chunk) {
					t.Fatalf("write = %d, %v", n, err)
				}
			}
			all := strings.Join(chunks, "")
			want := all
			if len(want) > 8 {
				if tail {
					want = want[len(want)-8:]
				} else {
					want = want[:8]
				}
			}
			if got := capture.text(); got != want || capture.omitted != (len(all) > 8) {
				t.Fatalf("tail=%t chunks=%q: got %q, omitted=%t; want %q", tail, chunks, got, capture.omitted, want)
			}
		}
	}
}

func TestMRunLineCapture(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		for _, source := range []string{"", "a", "a\n", "a\nb", "a\nb\n", "a\nb\nc", "\n\n\n", "α\r\nβ\nγ"} {
			capture := mrunCapture{maxLines: 2, tail: tail}
			// Byte-at-a-time writes exercise split UTF-8 and newline boundaries.
			for index := range len(source) {
				if _, err := capture.Write([]byte(source[index : index+1])); err != nil {
					t.Fatal(err)
				}
			}
			lines := strings.SplitAfter(source, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			omitted := len(lines) > 2
			if omitted {
				if tail {
					lines = lines[len(lines)-2:]
				} else {
					lines = lines[:2]
				}
			}
			want := strings.Join(lines, "")
			if got := capture.text(); got != want || capture.omitted != omitted {
				t.Fatalf("tail=%t source=%q: got %q omitted=%t; want %q omitted=%t", tail, source, got, capture.omitted, want, omitted)
			}
		}
	}
}

func TestMRunCombinedCaptureBoundsPendingLine(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		capture := mrunCapture{maxLines: 2, buffer: make([]byte, 132), tail: tail}
		chunk := []byte(strings.Repeat("a", 4096))
		for range 1000 {
			capture.Write(chunk)
			if capture.pending.Len() != 0 || capture.pendingBytes == nil ||
				capture.pendingBytes.size > len(capture.buffer) {
				t.Fatal("combined mode retained an unbounded pending line")
			}
		}
		capture.Write([]byte("END\nlast\n"))
		got := capture.text()
		if !capture.omitted || len(got) > len(capture.buffer) {
			t.Fatalf("tail=%t: bytes=%d omitted=%t", tail, len(got), capture.omitted)
		}
		if tail && !strings.HasSuffix(got, "END\nlast\n") {
			t.Fatalf("tail lost final line boundaries: %q", got)
		}
		if !tail && strings.Trim(got, "a") != "" {
			t.Fatalf("head lost initial bytes: %q", got)
		}
	}
}

func TestInvalidRowOptions(t *testing.T) {

	for _, flags := range []string{"-n", "-n 0", "-n -1", "-n 01", "-n 1.5", "-n 999999999999999999999", "-n 1 -n 2"} {
		if _, _, err := commandargs.ParseRunOptions(strings.Fields(flags + " -- echo")); err == nil {
			t.Fatalf("accepted %q", flags)
		}
	}
}

func TestMRunRetainedKindUsesBytePagingForOversizedOrUnterminatedRows(t *testing.T) {
	oversized := strings.Repeat("word ", 20000) + "\n"
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "ordinary rows", text: "first row\nsecond row\n", want: "rows"},
		{name: "oversized LF row", text: oversized, want: ""},
		{name: "unterminated bytes", text: "unterminated data", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mrunRetainedKind(tc.text); got != tc.want {
				t.Fatalf("retained kind = %q, want %q", got, tc.want)
			}
		})
	}
}
