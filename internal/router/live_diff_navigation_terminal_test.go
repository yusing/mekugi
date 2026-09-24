package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffTerminalFileNavigator(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, _, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	files := make([]mekugi.ReviewFile, 40)
	for i := range files {
		path := filepath.Join(workspace, fmt.Sprintf("internal/router/file%03d.go", i))
		files[i] = mekugi.RenderReviewFile("", path, "", strings.Repeat(fmt.Sprintf("file_%03d_content\n", i), 30))
	}
	id, err := store.reserveChange(t.Context(), workspace, "thread", "many")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"many": {ChangeID: id, CorrelationID: "many", Applied: true, ReviewFiles: files}}); err != nil {
		t.Fatal(err)
	}
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 22, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	ui.write(t, "vg\x1b[H")
	tree := ui.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "Files  40/40 · tree") &&
			!strings.Contains(liveDiffFrameRow(frame, 1), "Changes") &&
			strings.Contains(liveDiffFrameRow(frame, 3), "▼  internal/router")
	})
	var snapshot []string
	for row := 1; row <= 22; row++ {
		snapshot = append(snapshot, liveDiffFrameRow(tree, row))
	}
	t.Log("wide navigator frame:\n" + strings.Join(snapshot, "\n"))
	for row := 1; row <= 22; row++ {
		if ansi.StringWidth(liveDiffFrameRow(tree, row)) > 119 {
			t.Fatalf("overflow row %d", row)
		}
	}
	if strings.Contains(ansi.Strip(tree), "PATH | row") {
		t.Fatal("redundant file/row position header is still displayed")
	}
	fileRow := liveDiffFrameRow(tree, 4)
	if !strings.Contains(fileRow, "A  file000.go") || !strings.Contains(fileRow, "+30 -0") {
		t.Fatalf("file status and inline added/removed stats are missing: %q", fileRow)
	}
	rawFileRow, _, _ := strings.Cut(strings.Split(tree, "\x1b[4;1H")[1], "\x1b[5;1H")
	if !strings.Contains(rawFileRow, "\x1b[") {
		t.Fatalf("file status and inline stats lost their color styling: %q", rawFileRow)
	}
	// An empty filter submission keeps the dock focused, allowing keyboard
	// movement and selection without cycling focus with Tab.
	ui.write(t, "/\rj\r")
	keySelected := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "file_001_content") })
	if strings.Contains(ansi.Strip(keySelected), "file_000_content") {
		t.Fatal("keyboard selection did not open the highlighted file")
	}
	ui.write(t, "/\r\x1b[6~")
	paged := ui.frame(t, func(frame string) bool {
		for row := 2; row <= 22; row++ {
			line := liveDiffFrameRow(frame, row)
			if strings.Contains(line, ">") && strings.Contains(line, "file020.go") {
				return true
			}
		}
		return false
	})
	if !strings.Contains(ansi.Strip(paged), "file_001_content") {
		t.Fatal("file-list paging moved the diff viewport")
	}
	ui.write(t, "g")
	ui.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 3), "internal/router") && strings.Contains(liveDiffFrameRow(frame, 3), ">")
	})
	// Filtering narrows the persistent dock; Enter opens that file.
	ui.write(t, "/file023.go")
	ui.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 1), "1/40 · flat") })
	ui.write(t, "\r")
	selected := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "file_023_content") })
	if !strings.Contains(ansi.Strip(selected), "file_023_content") {
		t.Fatal("picker opened wrong diff")
	}
	ui.write(t, "\x15")
	ui.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "40/40 · tree") && strings.Contains(liveDiffFrameRow(frame, 3), "▼  internal/router")
	})
	// Pointer scrolling targets the list, not the selected file's viewport.
	ui.write(t, "\x1b[<65;5;10M")
	wheel := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	if !strings.Contains(ansi.Strip(wheel), "file_023_content") {
		t.Fatal("list wheel moved the selected diff viewport")
	}
	// Click another visible file, preserving the separate diff viewport.
	ui.write(t, "\x1b[<0;8;8M")
	clicked := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return !strings.Contains(plain, "file_023_content") && strings.Contains(plain, "file_005_content")
	})
	if strings.Contains(ansi.Strip(clicked), "file_023_content") {
		t.Fatal("mouse selection did not change the diff file")
	}
	// The dock is persistent at wide sizes and can be hidden and restored.
	ui.write(t, "s")
	hidden := ui.frame(t, func(frame string) bool { return !strings.Contains(liveDiffFrameRow(frame, 1), "Files") })
	if !strings.Contains(ansi.Strip(hidden), "file_005_content") {
		t.Fatal("hiding the file dock changed the diff selection")
	}
	ui.write(t, "s")
	ui.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 1), "Files") })
	// On narrow panes the dock becomes a full-width picker toggled by s.
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 22, Cols: 70}); err != nil {
		t.Fatal(err)
	}
	ui.frame(t, func(frame string) bool { return !strings.Contains(liveDiffFrameRow(frame, 1), "Files") })
	ui.write(t, "s")
	narrow := ui.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 1), "40/40") })
	for row := 1; row <= 22; row++ {
		if ansi.StringWidth(liveDiffFrameRow(narrow, row)) > 69 {
			t.Fatalf("narrow overflow row %d", row)
		}
	}
	ui.write(t, "s")
	ui.frame(t, func(frame string) bool { return !strings.Contains(liveDiffFrameRow(frame, 1), "Files") })
	// Empty Enter ends filter text entry but keeps navigation focused. The next
	// selection can be made directly from the picker, without Tab focus cycling.
	ui.write(t, "/\rj\r")
	opened := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "file_006_content") })
	if strings.Contains(ansi.Strip(opened), "file_005_content") {
		t.Fatal("keyboard selection in the narrow picker did not open the highlighted file")
	}
	if strings.Contains(liveDiffFrameRow(opened, 1), "Files") {
		t.Fatal("opening a file did not close the narrow picker")
	}
	ui.quit(t)
}

func TestLiveDiffHunkNavigationAndFileMemory(t *testing.T) {
	c := newLiveDiffTerminalController(nil, "", nil)
	defer c.close()
	c.diffMode = true
	c.files = []liveDiffFile{{Path: "a.go", Chunks: []liveDiffChunk{{Key: "a", Review: mekugi.ReviewFile{BeforePath: "a.go", AfterPath: "a.go", Diff: "@@ -1 +1 @@\n-old\n+new\n@@ -20 +20 @@\n-before\n+after\n"}}}}, {Path: "b.go", Chunks: []liveDiffChunk{{Key: "b", Review: mekugi.RenderReviewFile("", "b.go", "", "second\n")}}}}
	c.view.Files = c.files
	var err error
	c.rendering, err = c.renderer.Render(t.Context(), livediff.DarkTheme, c.files, "", 80, 0, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.rendering.Hunks) != 3 {
		t.Fatalf("hunk offsets: %v", c.rendering.Hunks)
	}
	c.navigation.rebuild(c.files, "")
	c.lines, c.rows = c.rendering.Lines, 8
	c.handleKey(']')
	first := c.view.Scroll[c.files[0].Key()]
	if first != c.rendering.Hunks[0] {
		t.Fatalf("next hunk at %d, want %d", first, c.rendering.Hunks[0])
	}
	c.offset = first
	c.handleKey(']')
	second := c.view.Scroll[c.files[0].Key()]
	if second != c.rendering.Hunks[1] {
		t.Fatalf("second hunk at %d", second)
	}
	c.handleKey('n')
	c.handleKey('p')
	if c.view.Selected != 0 || c.view.Scroll[c.files[0].Key()] != second {
		t.Fatal("file switching lost scroll memory")
	}
	c.offset = second
	c.handleKey('[')
	if c.view.Scroll[c.files[0].Key()] != first {
		t.Fatal("previous hunk missed anchor")
	}
}
