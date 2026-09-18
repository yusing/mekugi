package router

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
)

func previewViewFixture(id string, rows int) liveDiffPreview {
	var diff strings.Builder
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return liveDiffPreview{ID: id, Workspace: "/workspace", Thread: "thread",
		Input: diff.String()}
}

func TestLiveDiffPreviewPaneFollowAndLifecycle(t *testing.T) {
	var pane liveDiffPreviewPane
	for _, size := range []int{2, 30, 300, 2000} {
		pane.update(previewViewFixture("one", size))
		lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
		if err != nil || len(lines) > 12 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), fmt.Sprintf("+stream_%04d", size)) {
			t.Fatalf("stream tip %d escaped region: %v %q", size, err, lines)
		}
		if pane.views["one"].focus != size-1 {
			t.Fatalf("follow anchored to hunk start rather than tip: %d", pane.views["one"].focus)
		}
	}
	pane.update(liveDiffPreview{ID: "one"})
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
	if err != nil || !strings.Contains(lines[0], "STREAMING COMPLETE") {
		t.Fatalf("missing completion hold: %v %q", err, lines)
	}
	// Completed input stays visible until a new call replaces it.
	pane.update(previewViewFixture("two", 10))
	if len(pane.order) != 1 || pane.order[0] != "two" {
		t.Fatal("new call did not replace completed input")
	}
	pane.update(liveDiffPreview{ID: "two"})
	lines, err = pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
	if err != nil || !strings.Contains(lines[0], "STREAMING COMPLETE") || !strings.Contains(strings.Join(lines, "\n"), "stream_0010") {
		t.Fatalf("completed stream did not persist: %v %q", err, lines)
	}
}

func TestLiveDiffPreviewPaneLatestOnlyAndIndependent(t *testing.T) {
	var pane liveDiffPreviewPane
	for i := 1; i <= 500; i++ {
		pane.update(previewViewFixture("one", i))
	}
	if pane.views["one"].source != nil || pane.views["one"].rendered.ID != "" {
		t.Fatal("queued snapshots performed rendering")
	}
	lines, err := pane.render(t.Context(), "/workspace", liveDiffLightTheme, 80, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+stream_0500") {
		t.Fatalf("rendered stale queued snapshot: %v %q", err, lines)
	}
	source := pane.views["one"].source
	// Repaints (including captured-diff navigation) do not parse source again.
	_, err = pane.render(t.Context(), "/workspace", liveDiffLightTheme, 80, 10)
	if err != nil || &source[0] != &pane.views["one"].source[0] {
		t.Fatal("unchanged preview rebuilt its source")
	}
	pane.update(previewViewFixture("two", 20))
	pane.update(liveDiffPreview{ID: "two"})
	if pane.views["one"] == nil || pane.views["one"].complete {
		t.Fatal("one completed stream hid another active stream")
	}
}

func TestLiveDiffPreviewLayoutAndWrapping(t *testing.T) {
	var pane liveDiffPreviewPane
	preview := previewViewFixture("one", 1)
	preview.Input = strings.Repeat("界", 100) + "TIP\n"
	pane.update(preview)
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

func TestLiveDiffPreviewRepeatedSnapshotKeepsFocus(t *testing.T) {
	// A repeated snapshot must not move focus, because prepare reuses it.
	var pane liveDiffPreviewPane
	pane.update(previewViewFixture("one", 30))
	first, _ := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 80, 8)
	pane.update(previewViewFixture("one", 30))
	second, _ := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 80, 8)
	if !slices.Equal(first, second) {
		t.Fatal("identical snapshot moved the viewport")
	}
}

func BenchmarkLiveDiffPreviewPaneFrame(b *testing.B) {
	preview := previewViewFixture("one", 2000)
	var pane liveDiffPreviewPane
	base := preview.Input
	sequence := 0
	b.ReportAllocs()
	for b.Loop() {
		// Change the actual source so syntax work cannot hide behind cache hits.
		sequence++
		next := preview
		next.Input = strings.Replace(base, "stream_2000", fmt.Sprintf("stream_tip_%d", sequence), 1)
		pane.update(next)
		if _, err := pane.render(b.Context(), "/workspace", liveDiffDarkTheme, 120, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffPreviewSyntaxAndVisibleTip(t *testing.T) {
	preview := previewViewFixture("colored", 50)
	preview.Input = "#!python3\n" + strings.Repeat("# context\n", 49) + "return \"STREAM_TIP\"\n"
	var pane liveDiffPreviewPane
	pane.update(preview)
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
	pane.update(preview)
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 70, 12)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if err != nil || !strings.Contains(text, "STREAMING SCRIPT") || !strings.Contains(text, "package main") {
		t.Fatalf("missing mixed-script tail: %q, %v", text, err)
	}
	if strings.Contains(text, "stream.sh") || strings.Contains(text, "PREVIEW UNAVAILABLE") {
		t.Fatalf("raw script pretends to be a projected file: %q", text)
	}
}

func TestLiveDiffPreviewScriptSyntax(t *testing.T) {
	for _, theme := range []liveDiffTheme{liveDiffDarkTheme, liveDiffLightTheme} {
		for _, tc := range []struct {
			name, input, token string
			kind               chroma.TokenType
		}{
			{"bash", "if true; then\n  printf 'hello'\nfi\n", "if", chroma.Keyword},
			{"python", "#!python3\n" + strings.Repeat("# context\n", 100) + "return 'PYTHON_TIP'\n", "return", chroma.Keyword},
			{"uv_python", "#!uv run python\nreturn 'PYTHON_TIP'\n", "return", chroma.Keyword},
			{"javascript", "#!node\nconst tip = 'JS_TIP';\n", "const", chroma.KeywordDeclaration},
			{"batch", "echo first\n#!python3\nreturn 'BATCH_TIP'\n", "return", chroma.Keyword},
		} {
			t.Run(fmt.Sprintf("%d/%s", theme, tc.name), func(t *testing.T) {
				var pane liveDiffPreviewPane
				pane.update(liveDiffPreview{ID: tc.name, Workspace: "/workspace", Input: tc.input})
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
	paths := liveDiffSourceRows(input, liveDiffScriptSyntax(input))
	if len(paths) != 6 || paths[2].Path != "stream.sh" || paths[5].Path != "stream.py" {
		t.Fatalf("heredoc content changed language: %v", paths)
	}
}

func BenchmarkLiveDiffScriptPreviewFrame(b *testing.B) {
	for _, sourceName := range []string{"python", "shell"} {
		b.Run(sourceName, func(b *testing.B) {
			base := "#!python3\n" + strings.Repeat("# context\n", 2000)
			if sourceName == "shell" {
				base = strings.Repeat("echo context\n", 2000)
			}
			var pane liveDiffPreviewPane
			sequence := 0
			b.ReportAllocs()
			for b.Loop() {
				sequence++
				input := base + fmt.Sprintf("return \"tip_%d\"\n", sequence)
				pane.update(liveDiffPreview{ID: "stream", Workspace: "/workspace", Input: input})
				if _, err := pane.render(b.Context(), "/workspace", liveDiffDarkTheme, 120, 28); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestLiveDiffConcurrentPreviewCards(t *testing.T) {
	var pane liveDiffPreviewPane
	first := previewViewFixture("first", 100)
	first.Caller = "/root/editor"
	second := previewViewFixture("second", 200)
	second.Caller = "/root/reviewer"
	second.Thread = "another-thread"
	pane.update(first)
	pane.update(second)
	check := func(height int) string {
		t.Helper()
		lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 100, height)
		if err != nil || len(lines) > height {
			t.Fatalf("render: %v %q", err, lines)
		}
		return ansi.Strip(strings.Join(lines, "\n"))
	}
	frame := check(12)
	for _, want := range []string{"/root/editor · first", "/root/reviewer · second", "stream_0100", "stream_0200"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing concurrent caller/source %q: %s", want, frame)
		}
	}
	// Bursts cannot change call order or replace another call's source cache.
	otherSource := pane.views["second"].source
	for i := 101; i <= 150; i++ {
		first = previewViewFixture("first", i)
		first.Caller = "/root/editor"
		pane.update(first)
	}
	frame = check(12)
	if strings.Index(frame, "/root/editor") > strings.Index(frame, "/root/reviewer") ||
		!strings.Contains(frame, "stream_0150") || !strings.Contains(frame, "stream_0200") ||
		&otherSource[0] != &pane.views["second"].source[0] {
		t.Fatalf("delta reordered or replaced a concurrent view: %s", frame)
	}
	// Same-thread calls still have separate identities and completion states.
	second.Thread, second.Caller = first.Thread, first.Caller
	pane.update(second)
	pane.update(liveDiffPreview{ID: "first"})
	frame = check(12)
	if !strings.Contains(frame, "first · STREAMING COMPLETE") || !strings.Contains(frame, "second · STREAMING SCRIPT") {
		t.Fatalf("completion replaced another call: %s", frame)
	}
	if len(pane.order) != 2 || !pane.views["first"].complete || pane.views["second"].complete {
		t.Fatal("completion did not retain each call independently")
	}
	third := previewViewFixture("third", 300)
	pane.update(third)
	if frame := check(3); !strings.Contains(frame, "+1 more calls") {
		t.Fatalf("tiny pane silently hid a concurrent call: %s", frame)
	}
	if frame := check(12); !strings.Contains(frame, "stream_0200") || !strings.Contains(frame, "stream_0300") {
		t.Fatalf("resize did not restore concurrent tips: %s", frame)
	}
}

func TestLiveDiffPreviewCallerSafeAndBounded(t *testing.T) {
	var pane liveDiffPreviewPane
	preview := previewViewFixture("caller", 1)
	preview.Caller = "/root/\x1b[2J" + strings.Repeat("界", 80)
	pane.update(preview)
	for _, width := range []int{1, 8, 40, 80} {
		lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, width, 5)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			if strings.Contains(line, "\x1b[2J") || ansi.StringWidth(line) > width-1 {
				t.Fatalf("unsafe or unbounded caller: %q", line)
			}
		}
	}
}

func BenchmarkLiveDiffConcurrentPreviewFrame(b *testing.B) {
	var pane liveDiffPreviewPane
	for i := range 16 {
		pane.update(previewViewFixture(fmt.Sprint(i), 2000))
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := pane.render(b.Context(), "/workspace", liveDiffDarkTheme, 100, 28); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLiveDiffCompletedPreviewKeepsLastValidWarning(t *testing.T) {
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{
		ID: "one", Workspace: "/workspace", Thread: "thread",
		Status: "STREAMING PREVIEW: last valid diff; current edit unavailable",
	})
	pane.update(liveDiffPreview{ID: "one"})
	lines, err := pane.render(t.Context(), "/workspace", liveDiffDarkTheme, 160, 12)
	if err != nil {
		t.Fatal(err)
	}
	header := ansi.Strip(lines[0])
	for _, want := range []string{"STREAMING COMPLETE", "last valid diff; current edit unavailable"} {
		if !strings.Contains(header, want) {
			t.Fatalf("completed preview lost %q: %s", want, header)
		}
	}
}
