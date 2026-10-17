package router

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestTerminalViewMemory(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ctx = t.Context()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diffScreen.Close() })
	if err := u.paint(io.Discard, 120, 32); err != nil {
		t.Fatal(err)
	}
	if u.shell.diffScreen.Emulator != nil {
		t.Fatal("hidden Diff allocated a terminal parser")
	}
	if err := u.shell.diffScreen.Close(); err != nil {
		t.Fatal(err)
	}
	u.modelsLoading = true
	u.shell.DiffOpen = true
	u.shell.diff.Update((presentation.Publication{}).With(presentation.GroupCaptures([]presentation.Chunk{liveDiffCapture("main", "main.go", 1, "old\n", "new é\n", presentation.Origin{})})), "")
	var first string
	for _, width := range []int{120, 100, 120} {
		if err := u.paint(io.Discard, width, 32); err != nil {
			t.Fatal(err)
		}
		frame := u.shell.diffScreen.Render()
		plain := ansi.Strip(frame)
		if !strings.Contains(plain, "new é") || !strings.Contains(plain, "main.go") {
			t.Fatal("saved Diff did not render", frame)
		}
		if width == 120 {
			if first != "" && frame != first {
				t.Fatal("resize changed restored terminal cells or styles")
			}
			first = frame
		}
	}
}
