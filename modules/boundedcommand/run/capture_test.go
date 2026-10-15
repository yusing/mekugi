package run

import (
	"strings"
	"testing"
)

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
		if _, _, err := parseMRunArguments(strings.Fields(flags + " -- echo")); err == nil {
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
