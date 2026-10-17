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
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	if err := u.testPaint(io.Discard, 120, 32); err != nil {
		t.Fatal(err)
	}
	if terminalTest(u).Diff.Emulator != nil {
		t.Fatal("hidden Diff allocated a terminal parser")
	}
	if err := terminalTest(u).Diff.Close(); err != nil {
		t.Fatal(err)
	}
	u.modelsLoading = true
	terminalTest(u).DiffOpen = true
	savedPane(terminalTest(u)).Update((presentation.Publication{}).With(presentation.GroupCaptures([]presentation.Chunk{liveDiffCapture("main", "main.go", 1, "old\n", "new é\n", presentation.Origin{})})), "")
	var first string
	for _, width := range []int{120, 100, 120} {
		if err := u.testPaint(io.Discard, width, 32); err != nil {
			t.Fatal(err)
		}
		frame := terminalTest(u).Diff.Render()
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
