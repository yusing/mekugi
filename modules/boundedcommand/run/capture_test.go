package run

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget"
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
		for _, source := range []string{"", "a", "a\n", "a\nb", "a\nb\n", "a\nb\nc", "\n\n\n", "α\r\nβ\nγ", "a\xff\xfe\n\x80\x80b\n\xff", "first\n" + strings.Repeat("x", 70000) + "\nlast", "first\n" + strings.Repeat("x", 70000) + "\nlast\n"} {
			capture, err := newMRunRowCapture(2, tail)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { capture.close() })
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
			want := strings.ToValidUTF8(strings.Join(lines, ""), "\uFFFD")
			selected, err := capture.selectRows(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got, err := selected.omitted(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if got != want || capture.omitted != omitted {
				t.Fatalf("tail=%t source bytes=%d: got %d bytes omitted=%t; want %d bytes omitted=%t", tail, len(source), len(got), capture.omitted, len(want), omitted)
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
			if capture.pendingBytes == nil ||
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

func TestMRunRowFinalizationMatchesWholeSelection(t *testing.T) {
	for _, tail := range []bool{false, true} {
		for _, source := range []string{
			strings.Repeat("row\r\n", 20000), strings.Repeat("x", 100000),
			strings.Repeat("界", 30000) + "\nlast", "first\n" + strings.Repeat("x", 100000) + "\n",
			strings.Repeat("a", 32771) + "\xff\xff\xfe界\xc0\x80\nlast",
			strings.Repeat("x", mrunDeliveryTokens*4) + "\nlast\n",
		} {
			out, err := newMRunRowCapture(1000000, tail)
			if err != nil {
				t.Fatal(err)
			}
			errCapture, err := newMRunRowCapture(1000000, tail)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { out.close(); errCapture.close() })
			for offset := 0; offset < len(source); offset += 137 {
				chunk := []byte(source[offset:min(offset+137, len(source))])
				out.Write(chunk)
				errCapture.Write(chunk)
			}
			got, err := finalizeMRunRows(t.Context(), out, errCapture, 23)
			if err != nil {
				t.Fatal(err)
			}
			normalized := strings.ToValidUTF8(source, "\uFFFD")
			want, err := outputbudget.Select(t.Context(), mrunDeliveryTokens, "rows", normalized, normalized)
			if err != nil {
				t.Fatal(err)
			}
			if got.Stdout != want.Stdout || got.Stderr != want.Stderr || got.ExitCode != 23 || got.Delivery != nil {
				t.Fatalf("tail=%t: delivery differs, stdout=%d/%d stderr=%d/%d", tail, len(got.Stdout), len(want.Stdout), len(got.Stderr), len(want.Stderr))
			}
			if got.OmittedOutput == nil || got.Stdout+got.OmittedOutput.Stdout != normalized || got.Stderr+got.OmittedOutput.Stderr != normalized {
				t.Fatal("selected streams did not reconstruct normalized bytes")
			}
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
		{name: "compressible oversized LF row", text: strings.Repeat("representation ", 7000) + "\n", want: ""},
		{name: "unterminated bytes", text: "unterminated data", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mrunRetainedKind(tc.text); got != tc.want {
				t.Fatalf("retained kind = %q, want %q", got, tc.want)
			}
		})
	}
}
