package terminalview

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestLiveDiffRenderAllFiles(t *testing.T) {
	workspace := t.TempDir()
	var files []presentation.File
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(workspace, name+".txt")
		diff := "--- /dev/null\n+++ " + strconv.Quote(path) + "\n@@ -0,0 +1 @@\n+" + name + " content\n"
		files = append(files, presentation.File{Path: path, Chunks: []presentation.Chunk{{

			Review: diffmodel.ReviewFile{AfterPath: path, Diff: diff},
		}}})
	}
	render, err := new(Renderer).Render(t.Context(), diffrender.TerminalTheme, files, workspace, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(render.Starts) != 2 || render.Starts[0] != 0 || render.Starts[1] <= 0 {
		t.Fatalf("missing file boundaries: %v", render.Starts)
	}
	for i, name := range []string{"first", "second"} {
		end := len(render.Lines)
		if i+1 < len(files) {
			end = render.Starts[i+1]
		}
		text := ansi.Strip(strings.Join(render.Lines[render.Starts[i]:end], "\n"))
		if !strings.Contains(text, name+".txt") || !strings.Contains(text, name+" content") {
			t.Fatalf("file %d not rendered in its own section: %q", i, text)
		}
	}
}

func TestLiveDiffScrollAcrossFiles(t *testing.T) {
	view := View{
		Files:  []presentation.File{{Path: "first"}, {Path: "second"}},
		Scroll: make(map[string]int),
	}
	render := Render{Lines: make([]string, 20), Starts: []int{0, 10}}
	for _, tc := range []struct {
		offset, file, local int
	}{
		{9, 0, 9}, {10, 1, 0}, {13, 1, 3}, {9, 0, 9}, {-1, 0, 0}, {25, 1, 9},
	} {
		view.ScrollTo(render, tc.offset)
		if view.Selected != tc.file || view.Scroll[view.Files[tc.file].Key()] != tc.local {
			t.Fatalf("scroll %d: selected=%d positions=%v", tc.offset, view.Selected, view.Scroll)
		}
	}
	if view.Scroll["first"] != 0 {
		t.Fatal("scrolling another file lost the first file's saved position")
	}
}
