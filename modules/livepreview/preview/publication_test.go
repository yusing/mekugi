package preview

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestBoundPreviewEscapingAndLargeInput(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 47<<10), strings.Repeat("\x00", 10<<10), strings.Repeat("界", 64<<10)} {
		input := Preview{ID: "preview", Input: text}
		before := responses.MustMarshalJSON(input)
		got := boundPreview(input)
		if len(responses.MustMarshalJSON(got)) > 48<<10 || !utf8.ValidString(got.Input) || !strings.HasSuffix(text, got.Input) {
			t.Fatal("preview exceeded bound or changed source suffix")
		}
		if got.Truncated != (len(before) > 48<<10) || !got.Truncated && got.Input != text {
			t.Fatal("preview truncation changed")
		}
	}
}

func TestPreviewDiffWindow(t *testing.T) {
	const header = "--- \"x.go\"\n+++ \"x.go\"\n"
	const hunk = "@@ -1 +1 @@\n"
	const laterHunk = "@@ -100,3 +100,4 @@\n"
	for _, tc := range []struct {
		name  string
		diff  string
		limit int
		want  string
	}{
		{name: "empty", limit: 16},
		{name: "fits exactly", diff: header + hunk + "+new\n-old\n", limit: len(header + hunk + "+new\n-old\n"), want: header + hunk + "+new\n-old\n"},
		{name: "fits unterminated", diff: header + hunk + "+new", limit: 128, want: header + hunk + "+new"},
		{name: "short addition before deletion suffix", diff: header + hunk + "+new\n" + strings.Repeat("-remaining\n", 20), limit: len(header + hunk + "+new\n"), want: header + hunk + "+new\n"},
		{name: "addition before deletion suffix", diff: header + hunk + strings.Repeat("-old\n", 20) + "+new\n" + strings.Repeat("-remaining\n", 20), limit: len(header + hunk + "-old\n+new\n"), want: header + hunk + "-old\n+new\n"},
		{name: "no additions", diff: header + hunk + strings.Repeat("-old\n", 20) + " tail\n", limit: len(header + hunk + "-old\n tail\n"), want: header + hunk + "-old\n tail\n"},
		{name: "unterminated tail", diff: header + hunk + strings.Repeat("-old\n", 20) + " tail", limit: len(header + hunk + "-old\n tail"), want: header + hunk + "-old\n tail"},
		{name: "latest hunk", diff: header + hunk + strings.Repeat("-old\n", 20) + laterHunk + " context\n+new\n" + strings.Repeat("-remaining\n", 20), limit: len(header + laterHunk + " context\n+new\n"), want: header + laterHunk + " context\n+new\n"},
		{name: "source resembling headers", diff: header + hunk + strings.Repeat("-old\n", 20) + "--- source\n+++ source\n" + strings.Repeat("-remaining\n", 20), limit: len(header + hunk + "--- source\n+++ source\n"), want: header + hunk + "--- source\n+++ source\n"},
		{name: "long added UTF-8 row", diff: header + hunk + "+" + strings.Repeat("界", 20) + "\n" + strings.Repeat("-remaining\n", 20), limit: len(header+hunk) + 15, want: header + hunk + "+界界界界\n"},
		{name: "long unterminated UTF-8 row", diff: header + hunk + "+" + strings.Repeat("界", 20), limit: len(header+hunk) + 15, want: header + hunk + "+界界界界\n"},
		{name: "long deleted row", diff: header + hunk + "-" + strings.Repeat("x", 100) + "\n", limit: len(header+hunk) + 8, want: header + hunk + "-xxxxxx\n"},
		{name: "no hunk", diff: header + "+" + strings.Repeat("x", 100) + "\n", limit: len(header) + 8, want: header + "+xxxxxx\n"},
		{name: "headers exhaust window", diff: header + hunk + strings.Repeat("+new\n", 20), limit: len(header + hunk)},
		{name: "file headers exhaust window", diff: header + hunk + "+new\n", limit: len(header)},
		{name: "zero window", diff: header + hunk + "+new\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := diffmodel.ReviewFile{BeforePath: "x.go", AfterPath: "x.go", Diff: tc.diff}
			if got := previewDiffWindow(file, "", tc.limit); got != tc.want {
				t.Fatalf("window = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewDiffWindowDisplayHeaders(t *testing.T) {
	const header = "--- \"x.go\"\n+++ \"x.go\"\n"
	const hunk = "@@ -1 +1 @@\n"
	file := diffmodel.ReviewFile{BeforePath: "/workspace/x.go", AfterPath: "/workspace/x.go", Diff: "update \"/workspace/x.go\" -> \"/workspace/x.go\"\n--- \"/workspace/x.go\"\n+++ \"/workspace/x.go\"\n" + hunk + strings.Repeat("+new\n", 20)}
	want := header + hunk + "+new\n"
	if got := previewDiffWindow(file, "/workspace", len(want)); got != want {
		t.Fatalf("window = %q, want %q", got, want)
	}
}

func TestPreviewDiffWindowAllocationScaling(t *testing.T) {
	for _, size := range []int{256 << 10, 2 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			file := diffmodel.ReviewFile{BeforePath: "x.go", AfterPath: "x.go", Diff: "--- \"x.go\"\n+++ \"x.go\"\n@@ -1 +1 @@\n" + strings.Repeat("+new\n", size/5)}
			previewDiffWindow(file, "", 16<<10)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			const runs = 10
			for range runs {
				if got := previewDiffWindow(file, "", 16<<10); len(got) > 16<<10 || !strings.HasSuffix(got, "+new\n") {
					t.Fatal("source window changed")
				}
			}
			runtime.ReadMemStats(&after)
			// Path normalization copies the diff once. Clipping may allocate its
			// bounded output, but must not allocate an entry for every input row.
			allocated := (after.TotalAlloc - before.TotalAlloc) / runs
			t.Logf("clipping %d bytes allocated %d bytes per call", len(file.Diff), allocated)
			if allocated > uint64(len(file.Diff))+(96<<10) {
				t.Fatalf("clipping %d bytes allocated %d bytes per call", len(file.Diff), allocated)
			}
		})
	}
}

func BenchmarkBoundPreview(b *testing.B) {
	for _, size := range []int{16 << 10, 256 << 10, 2 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			p := Preview{ID: "a", Files: []diffmodel.ReviewFile{{BeforePath: "x.go", AfterPath: "x.go", Diff: "--- x.go\n+++ x.go\n@@ -1 +1 @@\n" + strings.Repeat("+new\n", size/5)}}}
			b.ReportAllocs()
			for b.Loop() {
				got := boundPreview(p)
				if got.ID != p.ID || size > 48<<10 && !got.Truncated {
					b.Fatal("preview identity or clipping changed")
				}
			}
		})
	}
}
