package terminalview

// Source: internal/livediff/livediff_test.go@[ebde9ad5]
import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

func testChunk(key, path, diff string) presentation.Chunk {
	return presentation.Chunk{
		Key: key, Review: diffmodel.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff},
	}
}

func TestViewMergeRefresh(t *testing.T) {
	var viewPublication presentation.Publication
	first := testChunk("first", "file.txt", "@@ -1 +1 @@\n-old\n+new\n")
	view := View{Scroll: make(map[string]int)}
	viewPublication = viewPublication.With(presentation.GroupCaptures([]presentation.Chunk{first}))
	view.Update(viewPublication)
	view.RefreshVisible()
	if len(view.Files) != 1 || len(view.Visible[view.Files[0].Key()].Chunks) != 1 {
		t.Fatalf("visible view = %#v", view.Visible)
	}
}

func TestRendererPreservesSourceAndTheme(t *testing.T) {
	chunk := testChunk("edit", "file.go", "@@ -1 +1 @@\n-return \"old\"\n+return \"new\"\n")
	render, err := new(Renderer).Render(t.Context(), diffrender.DarkTheme,
		[]presentation.File{{Path: "file.go", Chunks: []presentation.Chunk{chunk}}}, "", 80)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(text, `-return "old"`) || !strings.Contains(text, `+return "new"`) {
		t.Fatalf("rendered diff lost source: %q", text)
	}
	if !strings.Contains(strings.Join(render.Lines, "\n"), diffrender.DarkTheme.Foreground(chroma.LiteralString)) {
		t.Fatal("rendered Go source lost syntax color")
	}
}

func TestRendererOriginPreservesSourceMatchingCaller(t *testing.T) {
	chunk := testChunk("edit", "file.txt", "@@ -1 +1 @@\n-old\n+new\n")
	chunk.Origin = presentation.Origin{Change: "amber1", Caller: "/root/worker", Source: "/root/worker"}
	chunk.Status = "pending"
	renderer := Renderer{Caller: func(path string) (string, string) { return "Worker", "" }}
	render, err := renderer.Render(t.Context(), diffrender.TerminalTheme, []presentation.File{{Path: "file.txt", Chunks: []presentation.Chunk{chunk}}}, "", 120)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(text, "pending · Worker · /root/worker") {
		t.Fatalf("caller label changed recorded source: %q", text)
	}
}
