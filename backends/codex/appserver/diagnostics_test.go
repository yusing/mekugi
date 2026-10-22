package appserver

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestDiagnosticsDeduplicatesAcrossWriteChunks(t *testing.T) {
	const input = "first\nsecond\nfirst\nfirst\nunfinished"
	const want = "first (repeated 3 times)\nsecond\nunfinished"
	for _, size := range []int{1, 7, len(input)} {
		t.Run(fmt.Sprintf("chunk=%d", size), func(t *testing.T) {
			var diagnostics Diagnostics
			for offset := 0; offset < len(input); offset += size {
				chunk := input[offset:min(offset+size, len(input))]
				if n, err := diagnostics.Write([]byte(chunk)); err != nil || n != len(chunk) {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if got := diagnostics.Snapshot(); got != want {
				t.Fatalf("Snapshot = %q; want %q", got, want)
			}
			retained := diagnostics.Snapshot()
			_, _ = diagnostics.Write([]byte("\nunfinished\n"))
			if got := diagnostics.Snapshot(); got != "first (repeated 3 times)\nsecond\nunfinished (repeated 2 times)" || retained != want {
				t.Fatalf("partial completion or detached snapshot changed: current=%q retained=%q", got, retained)
			}
		})
	}
}

func TestDiagnosticsBoundsRetainedLinesBytesAndFragment(t *testing.T) {
	const omitted = "[earlier diagnostics omitted]\n"
	t.Run("lines", func(t *testing.T) {
		var diagnostics Diagnostics
		for i := range 300 {
			_, _ = fmt.Fprintf(&diagnostics, "line %03d\n", i)
		}
		var want strings.Builder
		want.WriteString(omitted)
		for i := 44; i < 300; i++ {
			fmt.Fprintf(&want, "line %03d\n", i)
		}
		if got := diagnostics.Snapshot(); got != strings.TrimSpace(want.String()) {
			t.Fatalf("line bound lost the retained tail: %q", got)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		var diagnostics Diagnostics
		var want strings.Builder
		want.WriteString(omitted)
		for i := range 10 {
			line := fmt.Sprintf("%d:%s", i, strings.Repeat("x", 8000))
			_, _ = diagnostics.Write([]byte(line + "\n"))
			if i >= 4 {
				want.WriteString(line + "\n")
			}
		}
		if got := diagnostics.Snapshot(); got != strings.TrimSpace(want.String()) {
			t.Fatalf("byte bound changed retained diagnostics: length=%d", len(got))
		}
	})
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("fragment/complete=%t", complete), func(t *testing.T) {
			var diagnostics Diagnostics
			_, _ = diagnostics.Write([]byte("discarded prefix"))
			tail := strings.Repeat("x", 8<<10)
			_, _ = diagnostics.Write([]byte("discarded too" + tail[:4000]))
			_, _ = diagnostics.Write([]byte(tail[4000:]))
			if complete {
				_, _ = diagnostics.Write([]byte("\n"))
			}
			if got := diagnostics.Snapshot(); got != omitted+tail {
				t.Fatalf("fragment bound = %d bytes; want %d", len(got), len(omitted+tail))
			}
		})
	}
	t.Run("oversized single write", func(t *testing.T) {
		var diagnostics Diagnostics
		_, _ = diagnostics.Write([]byte(strings.Repeat("x", 64<<10) + "\n"))
		if got := diagnostics.Snapshot(); got != omitted+strings.Repeat("x", 8<<10) {
			t.Fatalf("oversized write retained %d bytes", len(got))
		}
	})
}

func TestDiagnosticsSanitizesCompleteAndPartialLines(t *testing.T) {
	var diagnostics Diagnostics
	input := "\x1b]52;c;clipboard\a\x1b[2J\x1b[31mwarning\x1b[0m\t界\r\n\n\t\n\x1b]0;title\aunfinished\x00"
	for _, value := range []byte(input) {
		_, _ = diagnostics.Write([]byte{value})
	}
	if got := diagnostics.Snapshot(); got != "warning    界\nunfinished" {
		t.Fatalf("unsafe or damaged diagnostic: %q", got)
	}
	if got := (*Diagnostics)(nil).Snapshot(); got != "" {
		t.Fatalf("nil diagnostics = %q", got)
	}
}

func TestDiagnosticsConcurrentWriteAndSnapshot(t *testing.T) {
	var diagnostics Diagnostics
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 128 {
				if n, err := diagnostics.Write([]byte("shared warning\n")); n != len("shared warning\n") || err != nil {
					t.Errorf("Write = %d, %v", n, err)
				}
				if snapshot := diagnostics.Snapshot(); !strings.HasPrefix(snapshot, "shared warning") || strings.Count(snapshot, "shared warning") != 1 {
					t.Errorf("concurrent snapshot = %q", snapshot)
				}
			}
		})
	}
	workers.Wait()
	if got := diagnostics.Snapshot(); got != "shared warning (repeated 1024 times)" {
		t.Fatalf("concurrent writes lost repetitions: %q", got)
	}
}
