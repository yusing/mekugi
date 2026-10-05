package router

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestTerminalSelectionScrollBeyondViewport(t *testing.T) {
	for _, surface := range []string{"Main", "Activity", "btw"} {
		t.Run(surface, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
			u.shell.diffOpen, u.shell.journalOpen = false, false
			var lines []string
			for i := range 60 {
				lines = append(lines, fmt.Sprintf("row %02d", i))
			}
			text := "```\n" + strings.Join(lines, "\n") + "\n```"
			view := u.view
			if surface == "Activity" {
				view = u.agents
				view.agents = []activityPaneAgent{{Name: "/root/worker", Final: true}}
			}
			if surface == "btw" {
				u.btw = &appServerBTW{question: "side question", status: "completed", answer: []btwAnswer{{text: text}}}
			} else {
				if surface == "Activity" {
					for i := 0; i < len(lines); i += 4 {
						view.applyAppServerItem(true, "", "main", "main", "turn", fmt.Sprint(i), "item/completed", "", appServerItem{Type: "agentMessage", Text: "```\n" + strings.Join(lines[i:i+4], "\n") + "\n```"})
					}
				} else {
					view.applyAppServerItem(true, "", "main", "main", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: text})
				}
			}
			screen := vt.NewEmulator(120, 30)
			defer screen.Close()
			paint := func() {
				t.Helper()
				if err := u.paint(screen, 120, 30); err != nil {
					t.Fatal(err)
				}
			}
			paint()
			x, y := -1, -1
			for row, spans := range u.shell.paintedCopy {
				for _, span := range spans {
					if span.Text == "row 59" {
						x, y = span.Column+5, row
					}
				}
			}
			if x < 0 {
				t.Fatalf("last row not painted: %s", screen.String())
			}
			mouse := func(button, x, y int, release bool) {
				t.Helper()
				end := "M"
				if release {
					end = "m"
				}
				if err := u.shell.mouse(fmt.Sprintf("\x1b[<%d;%d;%d%s", button, x+1, y+1, end)); err != nil {
					t.Fatal(err)
				}
			}
			mouse(0, x, y, false)
			s := u.shell.selection
			if s == nil || s.screenRows == 0 {
				t.Fatal("selection did not retain off-screen source")
			}
			// Arrivals must not replace even off-screen source while selecting.
			if surface == "btw" {
				u.btw.answer[0].text = "replacement"
			}
			for attempts := 0; s.top > 0 && attempts < 30; attempts++ {
				mouse(64, x, y, false)
				paint()
			}
			if !strings.Contains(screen.String(), "row 00") || strings.Contains(screen.String(), "replacement") {
				t.Fatalf("scroll did not paint frozen document: %s", screen.String())
			}
			// Finish at the first source cell, above the original viewport.
			firstX, firstY := -1, -1
			for row := s.rect.y; row < s.rect.y+s.rect.h; row++ {
				for _, span := range s.copySource[s.documentY(row)] {
					if span.Text == "row 00" {
						firstX, firstY = span.Column, row
					}
				}
			}
			if firstX < 0 {
				t.Fatal("first source row not in scrolled viewport")
			}
			mouse(32, firstX, firstY, false)
			mouse(0, firstX, firstY, true)
			want := strings.Join(lines, "\n")
			if surface == "Activity" {
				var blocks []string
				for i := 0; i < len(lines); i += 4 {
					blocks = append(blocks, strings.Join(lines[i:i+4], "\n"))
				}
				want = strings.Join(blocks, "\n\n")
			}
			if got := s.text(); got != want {
				t.Fatalf("copied off-screen source = %q, want %q", got, want)
			}
			// Completed selection can scroll without changing its source range.
			if err := u.shell.send("\x1b[6~"); err != nil {
				t.Fatal(err)
			}
			if u.shell.selection != s || s.top == 0 || s.text() != want {
				t.Fatal("page scrolling lost completed selection")
			}
			u.shell.selectionAction('c')
			if want := base64.StdEncoding.EncodeToString([]byte(want)); !strings.Contains(u.shell.clipboard, want) {
				t.Fatal("clipboard did not include the whole selection")
			}
		})
	}
}

func TestOutputSelectionScrollBeyondViewport(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("row %02d", i))
	}
	u, output := outputSelectionFixture(strings.Join(lines, "\n")+"\n", true)
	x, y := outputSelectionPoint(t, u, "row 39")
	u.outputMouse(0, x+5, y, false)
	s := u.output.selection
	output.Write("new arrival\n")
	for attempts := 0; s.top > 0 && attempts < 30; attempts++ {
		u.outputMouse(64, x, y, false)
		drawOutputDialog(u)
	}
	x, y = outputSelectionPoint(t, u, "row 00")
	u.outputMouse(32, x, y, false)
	u.outputMouse(0, x, y, true)
	if got, want := s.text(), strings.Join(lines, "\n"); got != want {
		t.Fatalf("dialog copied = %q, want %q", got, want)
	}
	u.outputKey("\x1b[6~")
	if u.output.selection != s || s.top == 0 || u.output.follow {
		t.Fatal("dialog page scroll lost frozen selection")
	}
	u.outputKey("\x1b")
	u.outputKey("G")
	if frame := drawOutputDialog(u); !strings.Contains(frame, "new arrival") {
		t.Fatal("clearing selection did not restore live output")
	}
}

func TestDiffSelectionScrollFromEOFAndRestart(t *testing.T) {
	const width, height = 130, 12
	c := liveDiffChangesController(t, width, height, []livediff.Chunk{
		liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a.go", 40), livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
	})
	c.native = true
	c.view.Open(0)
	c.frame(t)
	c.view.ScrollTo(c.rendering, len(c.lines)-1)
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	c.stdout, c.dirty = screen, true
	c.frame(t)
	u := selectionTestUI(strings.Split(screen.Render(), "\n")...)
	u.width, u.paintedWidth = width, width
	u.layout = terminalLayout{diff: terminalRect{0, 0, width, height}}
	u.diff = c
	u.selectionMouse(0, width-2, c.sourceY+1, false)
	s := u.selection
	if s == nil || s.top != c.offset {
		t.Fatal("EOF offset was not preserved")
	}
	u.selectionMouse(64, width-2, c.sourceY+1, false)
	s.scrollKey("\x1b[H")
	first := -1
	for row := s.rect.y; row < s.rect.y+s.rect.h; row++ {
		if strings.Contains(ansi.Strip(s.rows[s.documentY(row)]), "a.go_line_00") {
			first = row
			break
		}
	}
	if first < 0 {
		t.Fatal("first diff line not visible after Home")
	}
	u.selectionMouse(32, c.sourceX, first, false)
	u.selectionMouse(0, c.sourceX, first, true)
	var want []string
	for i := range 40 {
		want = append(want, fmt.Sprintf("+a.go_line_%02d", i))
	}
	if got := s.text(); got != strings.Join(want, "\n") {
		t.Fatalf("EOF selection lost source rows: %q", got)
	}
	if description, _ := s.mentionDescription(); description != "diff hunk @amber1:1-40" {
		t.Fatalf("EOF reference = %q", description)
	}
	u.selectionMouse(0, c.sourceX, first, false)
	u.selectionMouse(32, width-2, first+1, false)
	u.selectionMouse(0, width-2, first+1, true)
	if got := u.selection.text(); got != strings.Join(want[:2], "\n") {
		t.Fatalf("restart selected underlying viewport: %q", got)
	}
}

func TestSelectionScrolledClickUsesDocumentTarget(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.shell.diffOpen, u.shell.journalOpen = false, false
	u.agents.agents = []activityPaneAgent{{Name: "main", Final: true}}
	for i := range 40 {
		text := fmt.Sprintf("prompt %02d", i)
		u.agents.applyAppServerItem(true, "", "main", "main", fmt.Sprint(i), fmt.Sprint(i), "item/completed", "", appServerItem{
			Type: "agentMessage", Text: "```\n" + text + strings.Repeat("\nmore source", 20) + "\n```",
		})
	}
	if err := u.paint(&strings.Builder{}, 120, 30); err != nil {
		t.Fatal(err)
	}
	if err := u.paint(&strings.Builder{}, 120, 30); err != nil {
		t.Fatal(err)
	}
	r := u.shell.layout.agents
	x, y := r.x+u.agents.feedLeft, r.y+u.agents.feedTop
	selectionTestDrag(t, u.shell, x, y, x+1, y)
	s := u.shell.selection
	if !s.scrollKey("\x1b[H") {
		t.Fatal("Home did not scroll frozen selection")
	}
	y = -1
	for row := s.rect.y; row < s.rect.y+s.rect.h; row++ {
		at := s.documentY(row) - s.rect.y
		if at < len(s.questions) && (s.questions[at] != 0 || s.snippets[at] != (liveActivitySnippet{})) && strings.Contains(ansi.Strip(s.rows[s.documentY(row)]), "prompt 00") {
			y = row
			break
		}
	}
	if y < 0 {
		t.Fatal("first prompt not available with its click target")
	}
	u.shell.selectionMouse(0, x, y, false)
	u.shell.selectionMouse(0, x, y, true)
	if u.shell.output == nil || !strings.Contains(fmt.Sprint(u.shell.output.pages), "prompt 00") {
		t.Fatal("scrolled prompt click used the old viewport target")
	}
}

func TestOutputSelectionEdgeDragScrolls(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("row %02d", i))
	}
	u, _ := outputSelectionFixture(strings.Join(lines, "\n")+"\n", false)
	u.outputKey("g")
	drawOutputDialog(u)
	x, y := outputSelectionPoint(t, u, "row 00")
	u.outputMouse(0, x, y, false)
	s := u.output.selection
	bottom := u.output.rect.y + u.output.chrome() - 1 + u.output.rows
	for range 50 {
		u.outputMouse(32, u.output.rect.x+u.output.rect.w-3, bottom, false)
	}
	u.outputMouse(0, u.output.rect.x+u.output.rect.w-3, bottom, true)
	if got := s.text(); got != strings.Join(lines, "\n") {
		t.Fatalf("edge drag did not select off-screen text: %q", got)
	}
}
