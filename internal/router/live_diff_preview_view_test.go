package router

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

func previewViewFixture(id string, rows int) liveDiffPreview {
	var diff strings.Builder
	fmt.Fprintf(&diff, "--- /dev/null\n+++ stream.go\n@@ -0,0 +1,%d @@\n", rows)
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return liveDiffPreview{ID: id, Workspace: "/workspace", Thread: "thread",
		Files: []mekugi.ReviewFile{{AfterPath: "/workspace/stream.go", Diff: diff.String()}}}
}

func TestLiveDiffPreviewPaneFollowAndLifecycle(t *testing.T) {
	now := time.Unix(100, 0)
	var pane liveDiffPreviewPane
	for _, size := range []int{2, 30, 300, 2000} {
		pane.update(previewViewFixture("one", size), now)
		lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
		if err != nil || len(lines) > 12 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), fmt.Sprintf("+stream_%04d", size)) {
			t.Fatalf("stream tip %d escaped region: %v %q", size, err, lines)
		}
		if pane.focus != size-1 {
			t.Fatalf("follow anchored to hunk start rather than tip: %d", pane.focus)
		}
	}
	pane.update(liveDiffPreview{ID: "one"}, now)
	if pane.expire(now.Add(liveDiffPreviewHideDelay - time.Nanosecond)) {
		t.Fatal("preview hid before its hold delay")
	}
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
	if err != nil || !strings.Contains(lines[0], "STREAMING COMPLETE") {
		t.Fatalf("missing completion hold: %v %q", err, lines)
	}
	// A new stream cancels a pending hide, even if the old timer fires.
	pane.update(previewViewFixture("two", 10), now.Add(100*time.Millisecond))
	if pane.expire(now.Add(liveDiffPreviewHideDelay)) {
		t.Fatal("old completion timer hid a new stream")
	}
	pane.update(liveDiffPreview{ID: "two"}, now.Add(time.Second))
	if !pane.expire(now.Add(time.Second+liveDiffPreviewHideDelay)) || pane.current.ID != "" {
		t.Fatal("completed region did not release its height")
	}
}

func TestLiveDiffPreviewPaneLatestOnlyAndIndependent(t *testing.T) {
	var pane liveDiffPreviewPane
	now := time.Unix(100, 0)
	for i := 1; i <= 500; i++ {
		pane.update(previewViewFixture("one", i), now)
	}
	if pane.source != nil || pane.rendered.ID != "" {
		t.Fatal("queued snapshots performed rendering")
	}
	lines, err := pane.render(t.Context(), "/workspace", liveDiffLightTheme, 80, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+stream_0500") {
		t.Fatalf("rendered stale queued snapshot: %v %q", err, lines)
	}
	source := pane.source
	// Repaints (including captured-diff navigation) do not parse source again.
	_, err = pane.render(t.Context(), "/workspace", liveDiffLightTheme, 80, 10)
	if err != nil || &source[0] != &pane.source[0] {
		t.Fatal("unchanged preview rebuilt its source")
	}
	pane.update(previewViewFixture("two", 20), now)
	pane.update(liveDiffPreview{ID: "two"}, now)
	if pane.current.ID != "one" || !pane.hideAt.IsZero() {
		t.Fatal("one completed stream hid another active stream")
	}
}

func TestLiveDiffPreviewLayoutAndWrapping(t *testing.T) {
	for _, body := range []int{1, 2, 3, 10, 20, 40, 100} {
		diff, stream := liveDiffRegionRows(body, 1000, true, false)
		if diff+stream != body || diff < 1 {
			t.Fatalf("invalid region heights for %d: %d %d", body, diff, stream)
		}
		if body >= 10 && diff != body*3/10 {
			t.Fatalf("not a fixed 3:7 split: %d %d", diff, stream)
		}
		if d, s := liveDiffRegionRows(body, 1000, false, false); d != body || s != 0 {
			t.Fatal("hidden preview retained height")
		}
	}
	var pane liveDiffPreviewPane
	preview := previewViewFixture("one", 1)
	preview.Files[0].Diff = "--- /dev/null\n+++ stream.go\n@@ -0,0 +1 @@\n+" + strings.Repeat("界", 100) + "TIP\n"
	pane.update(preview, time.Time{})
	for _, width := range []int{4, 10, 40, 120} {
		lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, width, 8)
		if err != nil || len(lines) > 8 {
			t.Fatalf("render at width %d: %v", width, err)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > width-1 {
				t.Fatalf("line escaped width %d: %q", width, line)
			}
		}
		if width >= 10 && !strings.Contains(ansi.Strip(strings.Join(lines, "")), "TIP") {
			t.Fatalf("wrapped stream tip lost at width %d: %q", width, lines)
		}
	}
}

func TestLiveDiffPreviewFocusIgnoresTrailingContext(t *testing.T) {
	before := []liveDiffPreviewRow{{1, '-', "old\n"}, {1, '+', "fir\n"}, {2, ' ', "context\n"}}
	after := []liveDiffPreviewRow{{1, '-', "old\n"}, {1, '+', "first\n"}, {2, '+', "second\n"}, {3, ' ', "context\n"}}
	if focus := liveDiffPreviewFocus(before, after); focus != 2 {
		t.Fatalf("focus=%d, want final streamed addition", focus)
	}
	// A repeated snapshot must not move focus, because prepare reuses it.
	var pane liveDiffPreviewPane
	pane.update(previewViewFixture("one", 30), time.Time{})
	first, _ := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 80, 8)
	pane.update(previewViewFixture("one", 30), time.Time{})
	second, _ := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 80, 8)
	if !slices.Equal(first, second) {
		t.Fatal("identical snapshot moved the viewport")
	}
}

func BenchmarkLiveDiffPreviewPaneFrame(b *testing.B) {
	preview := previewViewFixture("one", 2000)
	var pane liveDiffPreviewPane
	base := preview.Files[0]
	sequence := 0
	b.ReportAllocs()
	for b.Loop() {
		// Change the actual source so syntax work cannot hide behind cache hits.
		sequence++
		next := preview
		file := base
		file.Diff = strings.Replace(base.Diff, "stream_2000", fmt.Sprintf("stream_tip_%d", sequence), 1)
		next.Files = []mekugi.ReviewFile{file}
		pane.update(next, time.Time{})
		if _, err := pane.render(b.Context(), "/workspace", liveDiffDarkTheme, 120, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLiveDiffStreamingRender(b *testing.B) {
	preview := previewViewFixture("one", 2000)
	b.Run("whole_diff_previous_path", func(b *testing.B) {
		chunk := liveDiffChunk{key: "preview", status: "STREAMING PREVIEW", review: preview.Files[0]}
		files := []liveDiffFile{{path: "/workspace/stream.go", chunks: []liveDiffChunk{chunk}}}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := new(liveDiffRenderer).render(b.Context(), liveDiffDarkTheme, files, "/workspace", 120, 0, chunk); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("dedicated_viewport", func(b *testing.B) {
		var pane liveDiffPreviewPane
		b.ReportAllocs()
		for b.Loop() {
			pane.rendered = liveDiffPreview{}
			pane.update(preview, time.Time{})
			if _, err := pane.render(b.Context(), "/workspace", liveDiffDarkTheme, 120, 28); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestLiveDiffPreviewFollowReservesSpaceBeforeWrappedContext(t *testing.T) {
	preview := liveDiffPreview{
		ID: "wrapped-context", Workspace: "/workspace", Thread: "thread",
		Files: []mekugi.ReviewFile{{
			BeforePath: "/workspace/file.txt", AfterPath: "/workspace/file.txt",
			Diff: "--- file.txt\n+++ file.txt\n@@ -1,2 +1,2 @@\n-old\n+STREAM_TIP\n " + strings.Repeat("x", 500) + "\n",
		}},
	}
	var pane liveDiffPreviewPane
	pane.update(preview, time.Time{})
	for _, width := range []int{100, 40, 20} {
		lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, width, 8)
		if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "STREAM_TIP") {
			t.Fatalf("wrapped context hid the focus at width %d: %v %q", width, err, lines)
		}
	}
}

func TestLiveDiffPreviewSyntaxAndVisibleTip(t *testing.T) {
	preview := previewViewFixture("colored", 50)
	preview.Files[0].Diff = strings.Replace(preview.Files[0].Diff, "+stream_0050", "+return \"STREAM_TIP\"", 1)
	var pane liveDiffPreviewPane
	pane.update(preview, time.Time{})
	for _, theme := range []liveDiffTheme{liveDiffDarkTheme, liveDiffLightTheme} {
		lines, err := pane.render(t.Context(), "/workspace", theme, 70, 12)
		if err != nil {
			t.Fatal(err)
		}
		center := lines[len(lines)-1]
		if !strings.Contains(ansi.Strip(center), "STREAM_TIP") ||
			!strings.Contains(center, theme.foreground(chroma.Keyword)+"return") ||
			!strings.Contains(center, theme.foreground(chroma.LiteralString)) {
			t.Fatalf("tip not visible and colored: %q", center)
		}
		if strings.Contains(lines[0], "validated") || strings.Contains(lines[0], "applied") {
			t.Fatal("preview title contains redundant disclaimer")
		}
	}
}

func TestLiveDiffPreviewRawScript(t *testing.T) {
	var pane liveDiffPreviewPane
	preview := liveDiffPreview{ID: "mixed", Workspace: "/workspace", Thread: "thread",
		Input: "shell printf 'before'\nnew after.go\ntype <<PATCH\npackage main\n"}
	pane.update(preview, time.Time{})
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if err != nil || !strings.Contains(text, "STREAMING SCRIPT") || !strings.Contains(text, "package main") {
		t.Fatalf("missing mixed-script tail: %q, %v", text, err)
	}
	if strings.Contains(text, "stream.sh") || strings.Contains(text, "PREVIEW UNAVAILABLE") {
		t.Fatalf("raw script pretends to be a projected file: %q", text)
	}
}

func TestLiveDiffShellLayoutAndDelay(t *testing.T) {
	if liveDiffPreviewHideDelay != 1500*time.Millisecond {
		t.Fatal("preview hold must be 1.5 seconds")
	}
	for _, body := range []int{1, 2, 3, 10, 20, 100} {
		diff, preview := liveDiffRegionRows(body, 1000, true, true)
		if diff+preview != body || diff < 1 || (body >= 10 && diff != body*7/10) {
			t.Fatalf("shell layout: body=%d diff=%d preview=%d", body, diff, preview)
		}
	}
}

func TestLiveDiffScriptSource(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"shell", ""},
		{"shell echo hi", "echo hi"},
		{"shell <<EO", ""},
		{"shell <<EOF\nprintf hi\nEO", "printf hi\n"},
		{"shell <<EOF\nprintf hi\nEOF\n", "printf hi\n"},
		{"shell <<-'END'\n\tprintf hi\n\tEND\n", "printf hi\n"},
		{"shell <<OUTER\ncat <<INNER\nhello\nINNER\nOUTER\n", "cat <<INNER\nhello\nINNER\n"},
		{"shell echo hi\nnew a\ntype <<PATCH\nshell literal\nPATCH\nshell pwd", "echo hi\nnew a\ntype <<PATCH\nshell literal\nPATCH\npwd"},
	} {
		if got, _ := liveDiffScriptSource(tc.input); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestLiveDiffRecoveryHold(t *testing.T) {
	now := time.Unix(100, 0)
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{ID: "recovery", Workspace: "/workspace", Recovery: true, Input: "maple target \"new\""}, now)
	pane.update(liveDiffPreview{ID: "recovery"}, now)
	if pane.expire(now.Add(500*time.Millisecond - time.Nanosecond)) {
		t.Fatal("recovery disappeared before 0.5 seconds")
	}
	if !pane.expire(now.Add(500 * time.Millisecond)) {
		t.Fatal("recovery did not close after 0.5 seconds")
	}
}

func TestLiveDiffRecoveryTailLabel(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{ID: "recovery", Workspace: "/workspace", Recovery: true,
		Input: "raw correction tail", Truncated: true}, time.Now())
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 80, 14)
	if err != nil || !strings.Contains(ansi.Strip(lines[0]), "STREAMING RECOVERY · tail") {
		t.Fatalf("clipped recovery lost its label: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewEmptyAndSparseLayout(t *testing.T) {
	for _, shell := range []bool{false, true} {
		for _, body := range []int{1, 2, 10, 20, 40} {
			if diff, preview := liveDiffRegionRows(body, 0, true, shell); diff != 0 || preview != body {
				t.Fatalf("empty captured view wasted rows: body=%d diff=%d preview=%d", body, diff, preview)
			}
			if diff, preview := liveDiffRegionRows(body, 1, true, shell); diff != 1 || preview != body-1 {
				t.Fatalf("sparse captured view wasted rows: body=%d diff=%d preview=%d", body, diff, preview)
			}
		}
	}
}

func TestLiveDiffPreviewScriptSyntax(t *testing.T) {
	for _, theme := range []liveDiffTheme{liveDiffDarkTheme, liveDiffLightTheme} {
		for _, tc := range []struct {
			name, input, token string
			kind               chroma.TokenType
			recovery           bool
		}{
			{"bash", "if true; then\n  printf 'hello'\nfi\n", "if", chroma.Keyword, false},
			{"python", "#!python3\n" + strings.Repeat("# context\n", 100) + "return 'PYTHON_TIP'\n", "return", chroma.Keyword, false},
			{"uv_python", "#!uv run python\nreturn 'PYTHON_TIP'\n", "return", chroma.Keyword, false},
			{"javascript", "#!node\nconst tip = 'JS_TIP';\n", "const", chroma.KeywordDeclaration, false},
			{"batch", "echo first\n#!python3\nreturn 'BATCH_TIP'\n", "return", chroma.Keyword, false},
			{"recovery", "maple target \"old\"\nmaple value \"RECOVERY_TIP", "target", chroma.Keyword, true},
			{"recovery_row", "maple 12:abcd\n", "12:abcd", chroma.LiteralNumber, true},
		} {
			t.Run(fmt.Sprintf("%d/%s", theme, tc.name), func(t *testing.T) {
				var pane liveDiffPreviewPane
				pane.update(liveDiffPreview{ID: tc.name, Workspace: "/workspace", Input: tc.input, Recovery: tc.recovery}, time.Time{})
				lines, err := pane.render(t.Context(), "/workspace", theme, 100, 20)
				frame := strings.Join(lines, "\n")
				if err != nil || !strings.Contains(frame, theme.foreground(tc.kind)+tc.token) {
					t.Fatalf("missing %s syntax: %v %q", tc.name, err, frame)
				}
			})
		}
	}
}

func TestLiveDiffScriptLanguageBoundaries(t *testing.T) {
	input := "cat <<EOF\n#!python3\nreturn not_python\nEOF\n#!python3\nreturn True\n"
	paths := liveDiffSourceRows(input, liveDiffScriptSyntax(input, false))
	if len(paths) != 6 || paths[2].Path != "stream.sh" || paths[5].Path != "stream.py" {
		t.Fatalf("heredoc content changed language: %v", paths)
	}
}

func BenchmarkLiveDiffScriptPreviewFrame(b *testing.B) {
	for _, recovery := range []bool{false, true} {
		b.Run(fmt.Sprintf("recovery=%t", recovery), func(b *testing.B) {
			base := "#!python3\n" + strings.Repeat("# context\n", 2000)
			if recovery {
				base = strings.Repeat("maple target \"old\"\n", 2000)
			}
			var pane liveDiffPreviewPane
			sequence := 0
			b.ReportAllocs()
			for b.Loop() {
				sequence++
				input := base + fmt.Sprintf("return \"tip_%d\"\n", sequence)
				pane.update(liveDiffPreview{ID: "stream", Workspace: "/workspace", Input: input, Recovery: recovery}, time.Time{})
				if _, err := pane.render(b.Context(), "/workspace", liveDiffDarkTheme, 120, 28); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
