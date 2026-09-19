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
	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffNativeRows(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "file.txt",
		"@@ -9,3 +9,3 @@\n context\n-old\n+new\n \n", true)
	chunk.Status = ""
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{chunk}}}, "", 80, 0, chunk)
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
		chunk := liveDiffChunk{Review: review}
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: path, Chunks: []liveDiffChunk{chunk}}}, "", 90, 0, chunk)
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
	initial := liveDiffChunk{Key: "create", Applied: true,
		Review: mekugi.ReviewFile{AfterPath: "file.txt",
			Diff: "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,100 @@\n" + strings.Repeat("+content\n", 100)}}
	recent := liveDiffHighlightChunk("edit", "file.txt", "@@ -90 +90 @@\n-content\n+LATEST90\n", true)
	// Initial/resumed history has no recency split. The latest edit is deep
	// inside one composed creation hunk, not at its beginning.
	view := liveDiffView{Following: true}
	view.Merge([]liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{initial, recent}}})
	view.RefreshVisible()
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{view.Visible[view.Files[0].Key()]}, "", 90, 0, recent)
	if err != nil {
		t.Fatal(err)
	}
	for _, rows := range []int{1, 2, 3, 7} {
		offset := render.FollowOffset(rows)
		viewport := ansi.Strip(strings.Join(render.Lines[offset:min(len(render.Lines), offset+rows)], "\n"))
		if !strings.Contains(viewport, "LATEST90") {
			t.Fatalf("%d rows: follow hides the target: offset=%d viewport=%q", rows, offset, viewport)
		}
	}
}

func TestLiveDiffFollowCentersLatestRow(t *testing.T) {
	initial := liveDiffChunk{Key: "create", Applied: true,
		Review: mekugi.ReviewFile{AfterPath: "file.txt",
			Diff: "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,100 @@\n" + strings.Repeat("+content\n", 100)}}
	recent := liveDiffHighlightChunk("edit", "file.txt", "@@ -50 +50 @@\n-content\n+LATEST50\n", true)
	view := liveDiffView{Following: true}
	view.Merge([]liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{initial}}})
	view.Merge([]liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{initial, recent}}})
	view.RefreshVisible()
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{view.Visible[view.Files[0].Key()]}, "", 90, 0, recent)

	if err != nil {
		t.Fatal(err)
	}
	const rows = 18
	offset := render.FollowOffset(rows)
	center := ansi.Strip(render.Lines[offset+rows/2])
	if !strings.Contains(center, "▎") || !strings.Contains(center, "+LATEST50") {
		t.Fatalf("center is not the latest highlighted change: %q", center)
	}
	for _, index := range []int{offset, offset + rows - 1} {
		if text := ansi.Strip(render.Lines[index]); !strings.Contains(text, "content") {
			t.Fatalf("available surrounding context is missing at row %d: %q", index, text)
		}
	}
	if position := render.FocusRow - offset; position != rows/2 {
		t.Fatalf("latest row position = %d, want centered position %d", position, rows/2)
	}
}

func TestLiveDiffFollowShortViewStartsAtAvailableContext(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "file.txt",
		"@@ -1,3 +1,3 @@\n before\n-old\n+LATEST\n after\n", true)
	chunk.Highlighted = true
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme,
		[]liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{chunk}}}, "", 90, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	if offset := render.FollowOffset(40); offset != 0 {
		t.Fatalf("short view offset = %d, want available context from the top", offset)
	}
	if got := ansi.Strip(render.Lines[render.FocusRow]); !strings.Contains(got, "+LATEST") {
		t.Fatalf("focus is not the highlighted change: %q", got)
	}
}

func TestLiveDiffFollowPrefersLatestHighlightedRegion(t *testing.T) {
	path := "file.txt"
	older := liveDiffHighlightChunk("", path, "@@ -90 +90 @@\n-old\n+OLDER90\n", true)
	older.Highlighted = false
	latest := liveDiffHighlightChunk("", path, "@@ -20 +20 @@\n-old\n+LATEST20\n", true)
	latest.Highlighted = true
	// The raw capture coordinate can become ambiguous after composition. The
	// recency mark remains authoritative for which visible region to follow.
	focus := liveDiffHighlightChunk("latest", path, "@@ -90 +90 @@\n-old\n+raw latest\n", true)
	focus.Highlighted = true
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: path, Chunks: []liveDiffChunk{older, latest}}}, "", 90, 0, focus)

	if err != nil {
		t.Fatal(err)
	}
	focused := ansi.Strip(render.Lines[render.FocusRow])
	if !strings.Contains(focused, "LATEST20") {
		t.Fatalf("follow lost the latest highlighted region: row=%d %q", render.FocusRow, focused)
	}
}

func TestLiveDiffNativeFollowCentersPreparedTip(t *testing.T) {
	diff := "--- /dev/null\n+++ file.txt\n@@ -0,0 +1,40 @@\n" + strings.Repeat("+content\n", 40)
	chunk := liveDiffChunk{
		Key: "latest", Status: "amber1 prepared (application unconfirmed)",
		Review: mekugi.ReviewFile{AfterPath: "file.txt", Diff: diff},
	}
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{chunk}}}, "", 90, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	const rows = 18
	offset := render.FollowOffset(rows)
	if render.FocusRow-offset != rows-1 || render.FocusRow != len(render.Lines)-1 {
		t.Fatal("prepared new file did not fill the viewport through its final source row")
	}
	// Status remains available in the captured document, but must not pin
	// follow to the top of a long creation.
	document := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(document, chunk.Status) || !strings.Contains(document, "New file") {
		t.Fatal("prepared metadata disappeared")
	}
}

func TestLiveDiffNativeNoNewlineAndControls(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "unknown.extension",
		"@@ -1 +1 @@\n-before\n\\ No newline at end of file\n+after\x1b]52;c;secret\a\x1b[2J\x1b[31m\t界 é 👩‍💻\n\\ No newline at end of file\n", true)
	for _, width := range []int{1, 2, 3, 12, 36, 90} {
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: "unknown.extension", Chunks: []liveDiffChunk{chunk}}}, "", width, 0, chunk)
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
	before, after, err := new(liveDiffRenderer).ColorHunk(t.Context(), livediff.TerminalTheme, review, []mekugi.ReviewRow{
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
	if _, err := new(liveDiffRenderer).Render(ctx, livediff.TerminalTheme, nil, "", 80, 0, liveDiffChunk{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := new(livediff.Renderer).ColorSource(ctx, livediff.TerminalTheme, "file.go", "var x = 1\n"); !errors.Is(err, context.Canceled) {
		t.Fatalf("syntax cancellation: %v", err)
	}
	chunk := liveDiffHighlightChunk("edit", "file.go", "@@ -1 +1 @@\n+missing removal\n", true)
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Path: "file.go", Chunks: []liveDiffChunk{chunk}}}, "", 80, 0, liveDiffChunk{})
	if err == nil || len(render.Lines) != 0 {
		t.Fatalf("malformed capture returned a partial successful view: %+v %v", render, err)
	}
	chunk.Review.Diff = strings.Repeat("x", maxChangeReadBytes+1)
	if _, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, []liveDiffFile{{Chunks: []liveDiffChunk{chunk}}}, "", 80, 0, liveDiffChunk{}); err == nil {
		t.Fatal("unbounded source accepted")
	}
}

func TestLiveDiffHiddenFilesKeepNavigationAndHistory(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "visible.txt", "@@ -1 +1 @@\n-old\n+new\n", true)
	chunk.Status = ""
	files := []liveDiffFile{
		{Path: "reverted-first"},
		{Path: "visible.txt", Chunks: []liveDiffChunk{chunk}},
		{Path: "reverted-middle"},
		{Path: "deleted.txt", Chunks: []liveDiffChunk{{Review: mekugi.ReviewFile{
			BeforePath: "deleted.txt", Diff: "--- deleted.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-removed source\n",
		}}}},
		{Path: "reverted-last"},
	}
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.DarkTheme, files, "", 90, 4, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if strings.Contains(text, "reverted") || strings.Contains(text, "removed source") ||
		!strings.Contains(text, "1/2  visible.txt") || !strings.Contains(text, "2/2  deleted.txt") ||
		!strings.Contains(text, "Deleted file") || render.Counts[3].Removed != 1 {
		t.Fatalf("hidden/deleted presentation is wrong: %q", text)
	}
	view := liveDiffView{Files: files, Scroll: map[string]int{}}
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

func TestLiveDiffFollowFinalChangedRowAndFragment(t *testing.T) {
	for _, diff := range []string{
		"@@ -0,0 +1,40 @@\n" + strings.Repeat("+earlier\n", 39) + "+" + strings.Repeat("long ", 30) + "FINAL_TIP\n",
		"@@ -1,2 +1,40 @@\n-old\n" + strings.Repeat("+earlier\n", 38) + "+FINAL_TIP\n context\n",
		"@@ -1,40 +1 @@\n" + strings.Repeat("-earlier\n", 38) + "-FINAL_TIP\n context\n",
	} {
		chunk := liveDiffHighlightChunk("latest", "file.txt", diff, true)
		for _, width := range []int{40, 90} {
			render, err := new(liveDiffRenderer).Render(t.Context(), livediff.DarkTheme,
				[]liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{chunk}}}, "", width, 0, chunk)
			if err != nil {
				t.Fatal(err)
			}
			offset := render.FollowOffset(18)
			tip := ansi.Strip(render.Lines[render.FocusRow])
			if !strings.Contains(tip, "FINAL_TIP") || render.FocusRow < offset || render.FocusRow >= offset+18 {
				t.Fatalf("width %d lost the final changed row: %q", width, tip)
			}
			if offset+18 != len(render.Lines) {
				t.Fatalf("width %d left unused rows at EOF", width)
			}
		}
	}
}

func TestLiveDiffIncompleteHistory(t *testing.T) {
	chunk := liveDiffChunk{Key: "unreadable", Applied: true,
		Review: mekugi.RenderIncompleteReviewFile("file", "file", "permission denied")}
	view := liveDiffView{}
	view.Merge([]liveDiffFile{{Path: "file", Chunks: []liveDiffChunk{chunk}}})
	view.RefreshVisible()
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme,
		[]liveDiffFile{view.Visible[view.Files[0].Key()]}, "", 100, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(text, "counts unavailable") || !strings.Contains(text, "incomplete history") || strings.Contains(text, "+0 -0") {
		t.Fatalf("incomplete rendering: %s", text)
	}
}

func TestLiveDiffCompleteCaptureAfterIncompleteHistory(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		incomplete := liveDiffChunk{Key: "unreadable", Applied: true, CaptureOrder: 1,
			Review: mekugi.RenderIncompleteReviewFile("file", "file", "permission denied")}
		complete := liveDiffChunk{Key: "readable", Applied: true, CaptureOrder: 2,
			Review: mekugi.RenderReviewFile("file", "file", "before\n", "NEW KNOWN CONTENT\n")}
		view := liveDiffView{Reviewed: map[string]bool{"unreadable": acknowledged}}
		view.Merge([]liveDiffFile{{Path: "file", Chunks: []liveDiffChunk{incomplete, complete}}})
		view.RefreshVisible()
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme,
			[]liveDiffFile{view.Visible[view.Files[0].Key()]}, "", 100, 0, complete)
		if err != nil {
			t.Fatal(err)
		}
		text := ansi.Strip(strings.Join(render.Lines, "\n"))
		if !strings.Contains(text, "NEW KNOWN CONTENT") || strings.Contains(text, "incomplete history") == acknowledged {
			t.Fatalf("acknowledged=%t: %s", acknowledged, text)
		}
	}
}
