package router

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLiveDiffWrapAndResize(t *testing.T) {
	source := strings.Repeat("ab 界é  ", 12)
	chunk := liveDiffHighlightChunk("edit", "file.txt",
		"@@ -1,2 +1,2 @@\n "+source+"\n-old\n+new\n", true)
	chunk.Status = ""
	files := []liveDiffFile{{Path: "file.txt", Chunks: []liveDiffChunk{chunk}}}
	wrapped, err := new(liveDiffRenderer).Render(t.Context(), liveDiffDarkTheme, files, "", 22, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	// Context has no fill, so concatenation must preserve even trailing spaces.
	var restored strings.Builder
	for _, line := range wrapped.Lines[wrapped.RowStarts[1]:wrapped.RowStarts[2]] {
		if ansi.StringWidth(line) > 21 {
			t.Fatalf("overflow: %q", line)
		}
		_, text, ok := strings.Cut(ansi.Strip(line), "│ ")
		if !ok {
			t.Fatalf("missing fixed coordinate column: %q", line)
		}
		restored.WriteString(text)
	}
	if restored.String() != source {
		t.Fatalf("wrapped source changed: %q, want %q", restored.String(), source)
	}
	wide, err := new(liveDiffRenderer).Render(t.Context(), liveDiffDarkTheme, files, "", 200, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	view := liveDiffView{Files: files, Scroll: map[string]int{"file.txt": wrapped.RowStarts[1] + 2}}
	view.Reflow(wrapped, wide)
	if view.Scroll["file.txt"] != wide.RowStarts[1] {
		t.Fatal("widening lost the logical source row")
	}
	view.Reflow(wide, wrapped)
	if view.Scroll["file.txt"] != wrapped.RowStarts[1] {
		t.Fatal("narrowing lost the logical source row")
	}
}

func TestLiveDiffWrappedSyntaxAndFocus(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "file.go",
		"@@ -0,0 +1 @@\n+\""+strings.Repeat("abcdefgh", 15)+"\"\n", true)
	chunk.Status = ""
	chunk.Highlighted = true
	render, err := new(liveDiffRenderer).Render(t.Context(), liveDiffDarkTheme,
		[]liveDiffFile{{Path: "file.go", Chunks: []liveDiffChunk{chunk}}}, "", 30, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	if render.FocusRow != len(render.Lines)-1 {
		t.Fatalf("focus did not land on the final wrapped fragment: %+v", render)
	}
	color := liveDiffDarkTheme.Foreground(chroma.LiteralStringDouble)
	for _, line := range render.Lines[render.RowStarts[1]:] {
		if !strings.Contains(line, color) || !strings.Contains(line, liveDiffDarkTheme.RowBackground('+')) ||
			!strings.Contains(line, "▎") || !strings.HasSuffix(line, "\x1b[0m") {
			t.Fatalf("continuation lost standalone styling: %q", line)
		}
	}
}

func TestLiveDiffResizeReflowsSavedFiles(t *testing.T) {
	var files []liveDiffFile
	for _, name := range []string{"first", "second"} {
		path := strings.Repeat(name+"/", 8) + "file.txt"
		chunk := liveDiffHighlightChunk("edit", path,
			"@@ -1,2 +1,2 @@\n "+strings.Repeat("long source ", 20)+"\n-old\n+new\n", true)
		chunk.Status = strings.Repeat("prepared status ", 8)
		files = append(files, liveDiffFile{Path: path, Chunks: []liveDiffChunk{chunk}})
	}
	wide, err := new(liveDiffRenderer).Render(t.Context(), liveDiffDarkTheme, files, "", 90, 0, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := new(liveDiffRenderer).Render(t.Context(), liveDiffDarkTheme, files, "", 22, 0, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	if len(wide.RowStarts) != len(narrow.RowStarts) {
		t.Fatal("wrapped headings or status changed logical row identities")
	}
	view := liveDiffView{Files: files, Selected: 1, Scroll: make(map[string]int)}
	for i, file := range files {
		end := len(wide.Lines)
		if i+1 < len(files) {
			end = wide.Starts[i+1]
		}
		// Last source row, after headings, status, and wrapped context.
		view.Scroll[file.Key()] = end - 1 - wide.Starts[i]
	}
	view.Reflow(wide, narrow)
	for i, file := range files {
		end := len(narrow.Lines)
		if i+1 < len(files) {
			end = narrow.Starts[i+1]
		}
		if got := view.Scroll[file.Key()] + narrow.Starts[i]; got != end-1 {
			t.Fatalf("file %d resize moved anchor to %d, want %d", i, got, end-1)
		}
	}
}
