package router

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLiveDiffWrapAndPan(t *testing.T) {
	source := strings.Repeat("ab 界é  ", 12)
	chunk := liveDiffHighlightChunk("edit", "file.txt",
		"@@ -1,2 +1,2 @@\n "+source+"\n-old\n+new\n", true)
	chunk.status = ""
	files := []liveDiffFile{{path: "file.txt", chunks: []liveDiffChunk{chunk}}}
	wrapped, err := renderLiveDiff(t.Context(), liveDiffDarkTheme, files, "", 22, 0, chunk, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Context has no fill, so concatenation must preserve even trailing spaces.
	var restored strings.Builder
	for _, line := range wrapped.lines[wrapped.rowStarts[1]:wrapped.rowStarts[2]] {
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
	panned, err := renderLiveDiff(t.Context(), liveDiffDarkTheme, files, "", 22, 0, chunk, 4)
	if err != nil {
		t.Fatal(err)
	}
	if panned.rowStarts[2]-panned.rowStarts[1] != 1 {
		t.Fatal("panning did not unlock wrapping")
	}
	want := ansi.Strip(ansi.Cut(source, 4, 20))
	if got := ansi.Strip(panned.lines[panned.rowStarts[1]]); !strings.HasSuffix(got, want) {
		t.Fatalf("horizontal source slice = %q, want suffix %q", got, want)
	}
	view := liveDiffView{files: files, scroll: map[string]int{"file.txt": wrapped.rowStarts[1] + 2}}
	view.reflow(wrapped, panned)
	if view.scroll["file.txt"] != panned.rowStarts[1] {
		t.Fatal("unlock lost the logical source row")
	}
	view.reflow(panned, wrapped)
	if view.scroll["file.txt"] != wrapped.rowStarts[1] {
		t.Fatal("relock lost the logical source row")
	}
}

func TestLiveDiffWrappedSyntaxAndFocus(t *testing.T) {
	chunk := liveDiffHighlightChunk("edit", "file.go",
		"@@ -0,0 +1 @@\n+\""+strings.Repeat("abcdefgh", 15)+"\"\n", true)
	chunk.status = ""
	chunk.highlighted = true
	render, err := renderLiveDiff(t.Context(), liveDiffDarkTheme,
		[]liveDiffFile{{path: "file.go", chunks: []liveDiffChunk{chunk}}}, "", 30, 0, chunk, 0)
	if err != nil {
		t.Fatal(err)
	}
	if render.focusRow != render.rowStarts[1] {
		t.Fatalf("focus did not land on source: %+v", render)
	}
	color := liveDiffDarkTheme.foreground(chroma.LiteralStringDouble)
	for _, line := range render.lines[render.rowStarts[1]:] {
		if !strings.Contains(line, color) || !strings.Contains(line, liveDiffDarkTheme.rowBackground('+')) ||
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
		chunk.status = strings.Repeat("prepared status ", 8)
		files = append(files, liveDiffFile{path: path, chunks: []liveDiffChunk{chunk}})
	}
	wide, err := renderLiveDiff(t.Context(), liveDiffDarkTheme, files, "", 90, 0, liveDiffChunk{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := renderLiveDiff(t.Context(), liveDiffDarkTheme, files, "", 22, 0, liveDiffChunk{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide.rowStarts) != len(narrow.rowStarts) {
		t.Fatal("wrapped headings or status changed logical row identities")
	}
	view := liveDiffView{files: files, selected: 1, scroll: make(map[string]int)}
	for i, file := range files {
		end := len(wide.lines)
		if i+1 < len(files) {
			end = wide.starts[i+1]
		}
		// Last source row, after headings, status, and wrapped context.
		view.scroll[file.key()] = end - 1 - wide.starts[i]
	}
	view.reflow(wide, narrow)
	for i, file := range files {
		end := len(narrow.lines)
		if i+1 < len(files) {
			end = narrow.starts[i+1]
		}
		if got := view.scroll[file.key()] + narrow.starts[i]; got != end-1 {
			t.Fatalf("file %d resize/unlock moved anchor to %d, want %d", i, got, end-1)
		}
	}
}
