package livediff

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

func testChunk(key, path, diff string) Chunk {
	return Chunk{
		Key: key, Review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff},
	}
}

func TestViewMergeRefresh(t *testing.T) {
	first := testChunk("first", "file.txt", "@@ -1 +1 @@\n-old\n+new\n")
	view := View{Following: true, Scroll: make(map[string]int)}
	view.Merge(GroupCaptures([]Chunk{first}))
	view.RefreshVisible()
	if len(view.Files) != 1 || len(view.Visible[view.Files[0].Key()].Chunks) != 1 {
		t.Fatalf("visible view = %#v", view.Visible)
	}
}

func TestRendererPreservesSourceAndTheme(t *testing.T) {
	chunk := testChunk("edit", "file.go", "@@ -1 +1 @@\n-return \"old\"\n+return \"new\"\n")
	render, err := new(Renderer).Render(t.Context(), DarkTheme,
		[]File{{Path: "file.go", Chunks: []Chunk{chunk}}}, "", 80, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(text, `-return "old"`) || !strings.Contains(text, `+return "new"`) {
		t.Fatalf("rendered diff lost source: %q", text)
	}
	if !strings.Contains(strings.Join(render.Lines, "\n"), DarkTheme.Foreground(chroma.LiteralString)) {
		t.Fatal("rendered Go source lost syntax color")
	}
}

func TestShellCommandsAndColorSource(t *testing.T) {
	source := "VALUE=argument rg pattern file | head -10\n"
	commands := shellCommands(source)
	if len(commands) != 2 || commands[strings.Index(source, " rg ")+1] != "rg" || commands[strings.Index(source, "head")] != "head" {
		t.Fatalf("shell commands = %#v", commands)
	}
	lines, err := new(Renderer).ColorSource(t.Context(), DarkTheme, "stream.sh", source)
	if err != nil {
		t.Fatal(err)
	}
	colored := strings.Join(lines, "\n")
	if ansi.Strip(colored) != strings.TrimSuffix(source, "\n") ||
		!strings.Contains(colored, DarkTheme.Foreground(chroma.NameFunction)+"rg") {
		t.Fatalf("colored shell source = %q", colored)
	}
}

func TestRendererCacheIsolation(t *testing.T) {
	const source = "package p\n"
	first, second := new(Renderer), new(Renderer)
	if _, err := first.ColorSource(t.Context(), DarkTheme, "file.go", source); err != nil {
		t.Fatal(err)
	}
	if len(first.syntax) != 1 || len(second.syntax) != 0 {
		t.Fatalf("renderer caches leaked: first=%d second=%d", len(first.syntax), len(second.syntax))
	}
}
