package router

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// legacyHRunLineText preserves the join-then-bound implementation as a
// differential oracle, including its omission and malformed UTF-8 policy.
func legacyHRunLineText(capture *hrunCapture) string {
	if capture.pending.Len() > 0 || (capture.pendingBytes != nil && capture.pendingBytes.size > 0) {
		capture.finishLine()
	}
	value := strings.Join(capture.rows[capture.rowStart:], "") + strings.Join(capture.rows[:capture.rowStart], "")
	if limit := len(capture.buffer); limit > utf8.UTFMax && len(value) > limit {
		capture.omitted = true
		if capture.tail {
			value = value[len(value)-limit:]
		} else {
			value = value[:limit]
		}
		value = trimHRunBoundary(value, capture.tail)
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

func TestHRunRowWindowDifferential(t *testing.T) {
	inputs := []string{
		"", "a", "a\nb\nc\nd\n", "é界🙂\né界🙂\nlast",
		"\xff\x80abc\n\xfe\nlast\x80", strings.Repeat("界", 80) + "\nshort\nend",
		strings.Repeat("abc🙂\n", 50),
	}
	for _, tail := range []bool{false, true} {
		for _, limit := range []int{0, 4, 5, 8, 13, 132} {
			for _, lines := range []int{1, 2, 7, 100} {
				for _, chunk := range []int{1, 3, 1024} {
					for i, input := range inputs {
						t.Run(fmt.Sprintf("tail=%t/bytes=%d/lines=%d/chunk=%d/input=%d", tail, limit, lines, chunk, i), func(t *testing.T) {
							current := hrunCapture{maxLines: lines, buffer: make([]byte, limit), tail: tail}
							legacy := hrunCapture{maxLines: lines, buffer: make([]byte, limit), tail: tail}
							for start := 0; start < len(input); start += chunk {
								part := []byte(input[start:min(start+chunk, len(input))])
								current.Write(part)
								legacy.Write(part)
							}
							want := legacyHRunLineText(&legacy)
							if got := current.text(); got != want || current.omitted != legacy.omitted {
								t.Fatalf("got %q omitted=%t; want %q omitted=%t", got, current.omitted, want, legacy.omitted)
							}
						})
					}
				}
			}
		}
	}
}

func BenchmarkHRunRowWindow(b *testing.B) {
	for _, tail := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			b.Run(fmt.Sprintf("tail=%t/legacy=%t", tail, legacy), func(b *testing.B) {
				rows := make([]string, 1024)
				for i := range rows {
					rows[i] = strings.Repeat("x", 4095) + "\n"
				}
				capture := hrunCapture{maxLines: len(rows), rows: rows, rowStart: 511, buffer: make([]byte, 132), tail: tail}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if legacy {
						legacyHRunLineText(&capture)
					} else {
						capture.text()
					}
				}
			})
		}
	}
}
