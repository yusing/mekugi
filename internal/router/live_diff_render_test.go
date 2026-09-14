package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

func TestLiveDiffNativeRows(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "file.txt",
		"@@ -9,3 +9,3 @@\n context\n-old\n+new\n \n", true)
	chunk.status = ""
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{path: "file.txt", chunks: []liveDiffChunk{chunk}}}, "", 80, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, line := range render.lines[1:] {
		rows = append(rows, strings.TrimRight(ansi.Strip(line), " "))
	}
	want := []string{
		"   9│ context",
		"  10│-old",
		"  10│+new",
		"  11│",
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("inline coordinates or blank source rows changed: %q", rows)
	}
}

func TestLiveDiffCompactCoordinates(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		path := "file.txt"
		review := mekugi.ReviewFile{AfterPath: path,
			Diff: "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,20 @@\n" + strings.Repeat("+content\n", 20)}
		if deleted {
			review = mekugi.ReviewFile{BeforePath: path,
				Diff: "--- file.txt\n+++ /dev/null\n@@ -1,20 +0,0 @@\n" + strings.Repeat("-content\n", 20)}
		}
		chunk := liveDiffChunk{review: review}
		render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{path: path, chunks: []liveDiffChunk{chunk}}}, "", 90, 0, chunk)
		if err != nil {
			t.Fatal(err)
		}
		if deleted {
			if len(render.lines) != 1 || render.counts[0].removed != 20 {
				t.Fatalf("deleted source not omitted: %+v", render)
			}
			continue
		}
		for i, line := range render.lines[1:] {
			marker := "+"
			if deleted {
				marker = "-"
			}
			want := fmt.Sprintf("  %2d│%scontent", i+1, marker)
			if strings.TrimRight(ansi.Strip(line), " ") != want {
				t.Fatalf("absent side or excessive number padding: got %q, want %q", ansi.Strip(line), want)
			}
		}
	}
}

func TestLiveDiffFollowInsideCombinedHunk(t *testing.T) {
	initial := liveDiffChunk{key: "create", applied: true,
		review: mekugi.ReviewFile{AfterPath: "file.txt",
			Diff: "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,100 @@\n" + strings.Repeat("+content\n", 100)}}
	recent := liveDiffHighlightChunk("edit", "file.txt", "@@ -90 +90 @@\n-content\n+LATEST90\n", true)
	// Initial/resumed history has no recency split. The latest edit is deep
	// inside one composed creation hunk, not at its beginning.
	view := liveDiffView{following: true}
	view.merge([]liveDiffFile{{path: "file.txt", chunks: []liveDiffChunk{initial, recent}}})
	view.refreshVisible()
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{view.visible[view.files[0].key()]}, "", 90, 0, recent)
	if err != nil {
		t.Fatal(err)
	}
	for _, rows := range []int{1, 2, 3, 7} {
		offset := render.followOffset(rows)
		viewport := ansi.Strip(strings.Join(render.lines[offset:min(len(render.lines), offset+rows)], "\n"))
		if !strings.Contains(viewport, "LATEST90") {
			t.Fatalf("%d rows: follow hides the target: offset=%d viewport=%q", rows, offset, viewport)
		}
	}
}

func TestLiveDiffFollowCentersLatestRow(t *testing.T) {
	initial := liveDiffChunk{key: "create", applied: true,
		review: mekugi.ReviewFile{AfterPath: "file.txt",
			Diff: "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,100 @@\n" + strings.Repeat("+content\n", 100)}}
	recent := liveDiffHighlightChunk("edit", "file.txt", "@@ -50 +50 @@\n-content\n+LATEST50\n", true)
	view := liveDiffView{following: true}
	view.merge([]liveDiffFile{{path: "file.txt", chunks: []liveDiffChunk{initial}}})
	view.merge([]liveDiffFile{{path: "file.txt", chunks: []liveDiffChunk{initial, recent}}})
	view.refreshVisible()
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme,
		[]liveDiffFile{view.visible[view.files[0].key()]}, "", 90, 0, recent)
	if err != nil {
		t.Fatal(err)
	}
	const rows = 18
	offset := render.followOffset(rows)
	if position := render.focusRow - offset; position != rows/2 {
		t.Fatalf("latest row position = %d, want centered position %d", position, rows/2)
	}
}

func TestLiveDiffFollowPrefersLatestHighlightedRegion(t *testing.T) {
	path := "file.txt"
	older := liveDiffHighlightChunk("", path, "@@ -90 +90 @@\n-old\n+OLDER90\n", true)
	older.highlighted = false
	latest := liveDiffHighlightChunk("", path, "@@ -20 +20 @@\n-old\n+LATEST20\n", true)
	latest.highlighted = true
	// The raw capture coordinate can become ambiguous after composition. The
	// recency mark remains authoritative for which visible region to follow.
	focus := liveDiffHighlightChunk("latest", path, "@@ -90 +90 @@\n-old\n+raw latest\n", true)
	focus.highlighted = true
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme,
		[]liveDiffFile{{path: path, chunks: []liveDiffChunk{older, latest}}}, "", 90, 0, focus)
	if err != nil {
		t.Fatal(err)
	}
	focused := ansi.Strip(render.lines[render.focusRow])
	if !strings.Contains(focused, "LATEST20") {
		t.Fatalf("follow lost the latest highlighted region: row=%d %q", render.focusRow, focused)
	}
}

func TestLiveDiffNativeFollowKeepsPreparedCaption(t *testing.T) {
	diff := "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,40 @@\n" + strings.Repeat("+content\n", 40)
	chunk := liveDiffChunk{
		key: "latest", status: "hp_a1 prepared (application unconfirmed)",
		review: mekugi.ReviewFile{AfterPath: "file.txt", Diff: diff},
	}
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{path: "file.txt", chunks: []liveDiffChunk{chunk}}}, "", 90, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	const rows = 18
	offset := min(render.focusOffset, max(0, len(render.lines)-rows))
	viewport := ansi.Strip(strings.Join(render.lines[offset:offset+rows], "\n"))
	if !strings.Contains(viewport, chunk.status) || !strings.Contains(viewport, "New file") || !strings.Contains(viewport, "+content") {
		t.Fatalf("following hid application uncertainty: %q", viewport)
	}
}

func TestLiveDiffNativeNoNewlineAndControls(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "unknown.extension",
		"@@ -1 +1 @@\n-before\n\\ No newline at end of file\n+after\x1b]52;c;secret\a\x1b[2J\x1b[31m\t界 é 👩‍💻\n\\ No newline at end of file\n", true)
	for _, width := range []int{1, 2, 3, 12, 36, 90} {
		render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{path: "unknown.extension", chunks: []liveDiffChunk{chunk}}}, "", width, 0, chunk)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range render.lines {
			if ansi.StringWidth(line) > width-1 || liveDiffSafe(line, true) != line {
				t.Fatalf("width %d: unsafe or overflowing row: %q", width, line)
			}
		}
		if width == 90 {
			text := ansi.Strip(strings.Join(render.lines, "\n"))
			if strings.Count(text, `\ No newline at end of file`) != 2 ||
				!strings.Contains(text, "after    界 é 👩‍💻") || strings.Contains(text, "secret") {
				t.Fatalf("missing newline or safe source text: %q", text)
			}
		}
	}
}

func TestLiveDiffNativeSyntaxSidesAndMultiline(t *testing.T) {
	review := mekugi.ReviewFile{BeforePath: "file.go", AfterPath: "file.go"}
	before, after, err := liveDiffSyntax(t.Context(), liveDiffTerminalTheme, review, []mekugi.ReviewRow{
		{Kind: '-', Text: "/* removed comment\n"},
		{Kind: '-', Text: "still removed */\n"},
		{Kind: '+', Text: "return \"new\"\n"},
		{Kind: ' ', Text: "var count = 42\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 || len(after) != 2 ||
		!strings.Contains(before[1], liveDiffTerminalTheme.foreground(chroma.CommentMultiline)) ||
		!strings.Contains(after[0], liveDiffTerminalTheme.foreground(chroma.Keyword)+"return\x1b[39m") ||
		!strings.Contains(after[0], liveDiffTerminalTheme.foreground(chroma.LiteralString)) ||
		!strings.Contains(after[1], liveDiffTerminalTheme.foreground(chroma.LiteralNumberInteger)+"42\x1b[39m") {
		t.Fatalf("syntax state leaked across sides or rows: before=%q after=%q", before, after)
	}
}

func TestLiveDiffNativeSyntaxFallback(t *testing.T) {
	for _, source := range []string{"plain source\n\n", strings.Repeat("x", maxLiveDiffSyntaxBytes+1) + "\n"} {
		for _, path := range []string{"unknown.extension", "file.go"} {
			lines, err := liveDiffColorSource(t.Context(), liveDiffTerminalTheme, path, source)
			if err != nil || ansi.Strip(strings.Join(lines, "\n"))+"\n" != source {
				t.Fatalf("fallback lost source for %s: err=%v", path, err)
			}
		}
	}
}

func TestLiveDiffNativeLexerPanicFallsBack(t *testing.T) {
	original := lexers.GlobalLexerRegistry
	lexers.GlobalLexerRegistry = chroma.NewLexerRegistry()
	t.Cleanup(func() { lexers.GlobalLexerRegistry = original })
	lexers.Register(chroma.MustNewLexer(&chroma.Config{Name: "broken", Filenames: []string{"*.fixture"}}, func() chroma.Rules {
		return chroma.Rules{"root": {{Pattern: ".+", Type: chroma.Text, Mutator: chroma.MutatorFunc(func(*chroma.LexerState) error {
			return errors.New("invalid lexer state")
		})}}}
	}))
	const source = "complete source\n"
	lines, err := liveDiffColorSource(t.Context(), liveDiffTerminalTheme, "broken.fixture", source)
	if err != nil || strings.Join(lines, "\n")+"\n" != source {
		t.Fatalf("lexer failure changed source: %q %v", lines, err)
	}
}

func TestLiveDiffNativePartialSourceCorpus(t *testing.T) {
	for _, path := range []string{"file.go", "file.py", "file.js", "file.ts", "file.json", "file.yaml", "file.rb", "file.rs", "file.html", "file.raku", "file.sh"} {
		for _, source := range []string{"/* unterminated\n", "\"unfinished\n", "'''unfinished\n", "`unfinished\n", "<!--unfinished\n", "q:to/END/;\ntext\n", "{{[(\n", "\n\n"} {
			lines, err := liveDiffColorSource(t.Context(), liveDiffTerminalTheme, path, source)
			if err != nil || ansi.Strip(strings.Join(lines, "\n"))+"\n" != source {
				t.Fatalf("%s: partial source changed: %q %v", path, lines, err)
			}
		}
	}
}

func TestLiveDiffNativeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := renderLiveDiff(ctx, liveDiffTerminalTheme, nil, "", 80, 0, liveDiffChunk{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := liveDiffColorSource(ctx, liveDiffTerminalTheme, "file.go", "var x = 1\n"); !errors.Is(err, context.Canceled) {
		t.Fatalf("syntax cancellation: %v", err)
	}
	chunk := liveDiffHighlightChunk("edit", "file.go", "@@ -1 +1 @@\n+missing removal\n", true)
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{path: "file.go", chunks: []liveDiffChunk{chunk}}}, "", 80, 0, liveDiffChunk{})
	if err == nil || len(render.lines) != 0 {
		t.Fatalf("malformed capture returned a partial successful view: %+v %v", render, err)
	}
	chunk.review.Diff = strings.Repeat("x", maxChangeReadBytes+1)
	if _, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{chunks: []liveDiffChunk{chunk}}}, "", 80, 0, liveDiffChunk{}); err == nil {
		t.Fatal("unbounded source accepted")
	}
}

func TestLiveDiffHiddenFilesKeepNavigationAndHistory(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "visible.txt", "@@ -1 +1 @@\n-old\n+new\n", true)
	chunk.status = ""
	files := []liveDiffFile{
		{path: "reverted-first"},
		{path: "visible.txt", chunks: []liveDiffChunk{chunk}},
		{path: "reverted-middle"},
		{path: "deleted.txt", chunks: []liveDiffChunk{{review: mekugi.ReviewFile{
			BeforePath: "deleted.txt", Diff: "--- deleted.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-removed source\n",
		}}}},
		{path: "reverted-last"},
	}
	render, err := renderLiveDiff(t.Context(), liveDiffDarkTheme, files, "", 90, 4, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.lines, "\n"))
	if strings.Contains(text, "reverted") || strings.Contains(text, "removed source") ||
		!strings.Contains(text, "1/2  visible.txt") || !strings.Contains(text, "2/2  deleted.txt") ||
		!strings.Contains(text, "Deleted file") || render.counts[3].removed != 1 {
		t.Fatalf("hidden/deleted presentation is wrong: %q", text)
	}
	view := liveDiffView{files: files, scroll: map[string]int{}}
	for offset := range len(render.lines) {
		view.scrollTo(render, offset)
		want := 1
		if offset >= render.starts[3] {
			want = 3
		}
		if view.selected != want {
			t.Fatalf("offset %d selected hidden file %d", offset, view.selected)
		}
	}
	if len(view.files) != 5 {
		t.Fatal("presentation discarded retained history")
	}
}
