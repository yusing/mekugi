package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffNativeRows(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "file.txt",
		"@@ -9,3 +9,3 @@\n context\n-old\n+new\n \n")
	chunk.Status = ""
	render, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme, []livediff.File{{Path: "file.txt", Chunks: []livediff.Chunk{chunk}}}, "", 80)
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, line := range render.Lines[1:] {
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
		chunk := livediff.Chunk{Review: review}
		render, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme, []livediff.File{{Path: path, Chunks: []livediff.Chunk{chunk}}}, "", 90)
		if err != nil {
			t.Fatal(err)
		}
		if deleted {
			if len(render.Lines) != 1 || render.Counts[0].Removed != 20 {
				t.Fatalf("deleted source not omitted: %+v", render)
			}
			continue
		}
		for i, line := range render.Lines[1:] {
			want := fmt.Sprintf("  %2d│+content", i+1)
			if strings.TrimRight(ansi.Strip(line), " ") != want {
				t.Fatalf("absent side or excessive number padding: got %q, want %q", ansi.Strip(line), want)
			}
		}
	}
}

func TestLiveDiffNativeNoNewlineAndControls(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "unknown.extension",
		"@@ -1 +1 @@\n-before\n\\ No newline at end of file\n+after\x1b]52;c;secret\a\x1b[2J\x1b[31m\t界 é 👩‍💻\n\\ No newline at end of file\n")
	for _, width := range []int{1, 2, 3, 12, 36, 90} {
		render, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme, []livediff.File{{Path: "unknown.extension", Chunks: []livediff.Chunk{chunk}}}, "", width)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range render.Lines {
			if ansi.StringWidth(line) > width-1 || livediff.Safe(line, true) != line {
				t.Fatalf("width %d: unsafe or overflowing row: %q", width, line)
			}
		}
		if width == 90 {
			text := ansi.Strip(strings.Join(render.Lines, "\n"))
			if strings.Count(text, `\ No newline at end of file`) != 2 ||
				!strings.Contains(text, "after    界 é 👩‍💻") || strings.Contains(text, "secret") {
				t.Fatalf("missing newline or safe source text: %q", text)
			}
		}
	}
}

func TestLiveDiffNativeSyntaxSidesAndMultiline(t *testing.T) {
	review := mekugi.ReviewFile{BeforePath: "file.go", AfterPath: "file.go"}
	before, after, err := new(livediff.Renderer).ColorHunk(t.Context(), livediff.TerminalTheme, review, []mekugi.ReviewRow{
		{Kind: '-', Text: "/* removed comment\n"},
		{Kind: '-', Text: "still removed */\n"},
		{Kind: '+', Text: "return \"new\"\n"},
		{Kind: ' ', Text: "var count = 42\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 || len(after) != 2 ||
		!strings.Contains(before[1], livediff.TerminalTheme.Foreground(chroma.CommentMultiline)) ||
		!strings.Contains(after[0], livediff.TerminalTheme.Foreground(chroma.Keyword)+"return\x1b[39m") ||
		!strings.Contains(after[0], livediff.TerminalTheme.Foreground(chroma.LiteralString)) ||
		!strings.Contains(after[1], livediff.TerminalTheme.Foreground(chroma.LiteralNumberInteger)+"42\x1b[39m") {
		t.Fatalf("syntax state leaked across sides or rows: before=%q after=%q", before, after)
	}
}

func TestLiveDiffNativeSyntaxFallback(t *testing.T) {
	for _, source := range []string{"plain source\n\n", strings.Repeat("x", livediff.MaxSyntaxBytes+1) + "\n"} {
		for _, path := range []string{"unknown.extension", "file.go"} {
			lines, err := new(livediff.Renderer).ColorSource(t.Context(), livediff.TerminalTheme, path, source)
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
	lines, err := new(livediff.Renderer).ColorSource(t.Context(), livediff.TerminalTheme, "broken.fixture", source)
	if err != nil || strings.Join(lines, "\n")+"\n" != source {
		t.Fatalf("lexer failure changed source: %q %v", lines, err)
	}
}

func TestLiveDiffNativePartialSourceCorpus(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"file.go", "file.py", "file.js", "file.ts", "file.json", "file.yaml", "file.rb", "file.rs", "file.html", "file.raku", "file.sh"} {
		for _, source := range []string{"/* unterminated\n", "\"unfinished\n", "'''unfinished\n", "`unfinished\n", "<!--unfinished\n", "q:to/END/;\ntext\n", "{{[(\n", "\n\n"} {
			lines, err := new(livediff.Renderer).ColorSource(t.Context(), livediff.TerminalTheme, path, source)
			if err != nil || ansi.Strip(strings.Join(lines, "\n"))+"\n" != source {
				t.Fatalf("%s: partial source changed: %q %v", path, lines, err)
			}
		}
	}
}

func TestLiveDiffNativeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := new(livediff.Renderer).Render(ctx, livediff.TerminalTheme, nil, "", 80); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := new(livediff.Renderer).ColorSource(ctx, livediff.TerminalTheme, "file.go", "var x = 1\n"); !errors.Is(err, context.Canceled) {
		t.Fatalf("syntax cancellation: %v", err)
	}
	chunk := liveDiffHighlightChunk("edit", "file.go", "@@ -1 +1 @@\n+missing removal\n")
	render, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme, []livediff.File{{Path: "file.go", Chunks: []livediff.Chunk{chunk}}}, "", 80)
	if err == nil || len(render.Lines) != 0 {
		t.Fatalf("malformed capture returned a partial successful view: %+v %v", render, err)
	}
	chunk.Review.Diff = strings.Repeat("x", maxChangeReadBytes+1)
	if _, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme, []livediff.File{{Chunks: []livediff.Chunk{chunk}}}, "", 80); err == nil {
		t.Fatal("unbounded source accepted")
	}
}

func TestLiveDiffHiddenFilesKeepNavigationAndHistory(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "visible.txt", "@@ -1 +1 @@\n-old\n+new\n")
	chunk.Status = ""
	files := []livediff.File{
		{Path: "reverted-first"},
		{Path: "visible.txt", Chunks: []livediff.Chunk{chunk}},
		{Path: "reverted-middle"},
		{Path: "deleted.txt", Chunks: []livediff.Chunk{{Review: mekugi.ReviewFile{
			BeforePath: "deleted.txt", Diff: "--- deleted.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-removed source\n",
		}}}},
		{Path: "reverted-last"},
	}
	render, err := new(livediff.Renderer).Render(t.Context(), livediff.DarkTheme, files, "", 90)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if strings.Contains(text, "reverted") || strings.Contains(text, "removed source") ||
		!strings.Contains(text, "1/2  visible.txt") || !strings.Contains(text, "2/2  deleted.txt") ||
		!strings.Contains(text, "Deleted file") || render.Counts[3].Removed != 1 {
		t.Fatalf("hidden/deleted presentation is wrong: %q", text)
	}
	view := livediff.View{Files: files, Scroll: map[string]int{}}
	for offset := range len(render.Lines) {
		view.ScrollTo(render, offset)
		want := 1
		if offset >= render.Starts[3] {
			want = 3
		}
		if view.Selected != want {
			t.Fatalf("offset %d selected hidden file %d", offset, view.Selected)
		}
	}
	if len(view.Files) != 5 {
		t.Fatal("presentation discarded retained history")
	}
}

func TestLiveDiffIncompleteHistory(t *testing.T) {
	chunk := livediff.Chunk{Key: "unreadable", Review: mekugi.RenderIncompleteReviewFile("file", "file", "permission denied")}
	view := livediff.View{}
	view.Merge([]livediff.File{{Path: "file", Chunks: []livediff.Chunk{chunk}}})
	view.RefreshVisible()
	render, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme,
		[]livediff.File{view.Visible[view.Files[0].Key()]}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(text, "counts unavailable") || !strings.Contains(text, "incomplete history") || strings.Contains(text, "+0 -0") {
		t.Fatalf("incomplete rendering: %s", text)
	}
}

func TestLiveDiffCompleteCaptureAfterIncompleteHistory(t *testing.T) {
	{
		incomplete := livediff.Chunk{Key: "unreadable", CaptureOrder: 1,
			Review: mekugi.RenderIncompleteReviewFile("file", "file", "permission denied")}
		complete := livediff.Chunk{Key: "readable", CaptureOrder: 2,
			Review: mekugi.RenderReviewFile("file", "file", "before\n", "NEW KNOWN CONTENT\n")}
		view := livediff.View{}
		view.Merge([]livediff.File{{Path: "file", Chunks: []livediff.Chunk{incomplete, complete}}})
		view.RefreshVisible()
		render, err := new(livediff.Renderer).Render(t.Context(), livediff.TerminalTheme,
			[]livediff.File{view.Visible[view.Files[0].Key()]}, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		text := ansi.Strip(strings.Join(render.Lines, "\n"))
		if !strings.Contains(text, "NEW KNOWN CONTENT") || !strings.Contains(text, "incomplete history") {
			t.Fatalf("incomplete history missing: %s", text)
		}
	}
}
