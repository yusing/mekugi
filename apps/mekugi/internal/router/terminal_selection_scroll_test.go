package router

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestTerminalSelectionScrollBeyondViewport(t *testing.T) {
	for _, surface := range []string{"Main", "Activity", "btw"} {
		t.Run(surface, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diffScreen.Close() })
			u.shell.DiffOpen = false
			var lines []string
			for i := range 60 {
				lines = append(lines, fmt.Sprintf("row %02d", i))
			}
			text := "```\n" + strings.Join(lines, "\n") + "\n```"
			view := u.view
			if surface == "Activity" {
				view = u.agents
				view.Agents = []activityPaneAgent{{Name: "/root/worker", Final: true}}
			}
			if surface == "btw" {
				u.btw = &appServerBTW{SideAnswer: &conversationmodel.SideAnswer{Question: "side question", Status: "completed", Answer: []conversationmodel.Answer{{Text: text}}}}
			} else {
				if surface == "Activity" {
					for i := 0; i < len(lines); i += 4 {
						applyTranscriptFixture(view, true, "", "main", "main", "turn", fmt.Sprint(i), "item/completed", "", appServerItem{Type: "agentMessage", Text: "```\n" + strings.Join(lines[i:i+4], "\n") + "\n```"})
					}
				} else {
					applyTranscriptFixture(view, true, "", "main", "main", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: text})
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
			if surface == "Activity" {
				u.agents.setDisplayFilter(true, "Main")
			}
			paint()
			x, y := -1, -1
			for row, spans := range u.shell.frame.Copy {
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
			if s == nil || s.ScreenRows() == 0 {
				t.Fatal("selection did not retain off-screen source")
			}
			// Arrivals must not replace even off-screen source while selecting.
			if surface == "btw" {
				u.btw.Answer[0].Text = "replacement"
			}
			for attempts := 0; s.Top() > 0 && attempts < 30; attempts++ {
				mouse(64, x, y, false)
				paint()
			}
			if !strings.Contains(screen.String(), "row 00") || strings.Contains(screen.String(), "replacement") {
				t.Fatalf("scroll did not paint frozen document: %s", screen.String())
			}
			// Finish at the first source cell, above the original viewport.
			firstX, firstY := -1, -1
			for row := s.Rect.Y; row < s.Rect.Y+s.Rect.H; row++ {
				for _, span := range s.CopySource[s.DocumentY(row)] {
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
			if got := s.Text(); got != want {
				t.Fatalf("copied off-screen source = %q, want %q", got, want)
			}
			// Completed selection can scroll without changing its source range.
			if err := u.shell.send("\x1b[6~"); err != nil {
				t.Fatal(err)
			}
			if u.shell.selection != s || s.Top() == 0 || s.Text() != want {
				t.Fatal("page scrolling lost completed selection")
			}
			u.shell.selectionAction('c')
			if want := base64.StdEncoding.EncodeToString([]byte(want)); !strings.Contains(u.shell.frame.Clipboard, want) {
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
	s := u.output.Selection
	output.Write("new arrival\n")
	for attempts := 0; s.Top() > 0 && attempts < 30; attempts++ {
		u.outputMouse(64, x, y, false)
		drawOutputDialog(u)
	}
	x, y = outputSelectionPoint(t, u, "row 00")
	u.outputMouse(32, x, y, false)
	u.outputMouse(0, x, y, true)
	if got, want := s.Text(), strings.Join(lines, "\n"); got != want {
		t.Fatalf("dialog copied = %q, want %q", got, want)
	}
	u.outputKey("\x1b[6~")
	if u.output.Selection != s || s.Top() == 0 {
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
	c := liveDiffChangesPane(t, width, height, []presentation.Chunk{
		liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a.go", 40), presentation.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
	})
	c.Key("g")
	for range len(c.Selection(width-2, height-1).Document) {
		c.Key("j")
	}
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	c.Invalidate()
	if err := c.Paint(t.Context(), screen, width, height); err != nil {
		t.Fatal(err)
	}
	source := c.Selection(width-2, height-1)
	u := selectionTestUI(strings.Split(screen.Render(), "\n")...)
	u.width, u.frame.Width = width, width
	u.layout = terminalsession.Layout{Diff: terminalsession.Rect{0, 0, width, height}}
	u.diff = c
	u.selectionMouse(0, width-2, source.Y+1, false)
	s := u.selection
	if s == nil || s.Top() != source.Offset {
		t.Fatal("EOF offset was not preserved")
	}
	u.selectionMouse(64, width-2, source.Y+1, false)
	s.ScrollKey("\x1b[H")
	first := -1
	for row := s.Rect.Y; row < s.Rect.Y+s.Rect.H; row++ {
		if strings.Contains(ansi.Strip(s.Rows[s.DocumentY(row)]), "a.go_line_00") {
			first = row
			break
		}
	}
	if first < 0 {
		t.Fatal("first diff line not visible after Home")
	}
	u.selectionMouse(32, source.X, first, false)
	u.selectionMouse(0, source.X, first, true)
	var want []string
	for i := range 40 {
		want = append(want, fmt.Sprintf("+a.go_line_%02d", i))
	}
	if got := s.Text(); got != strings.Join(want, "\n") {
		t.Fatalf("EOF selection lost source rows: %q", got)
	}
	if description, _ := s.mentionDescription(); description != "diff hunk @amber1:1-40" {
		t.Fatalf("EOF reference = %q", description)
	}
	u.selectionMouse(0, source.X, first, false)
	u.selectionMouse(32, width-2, first+1, false)
	u.selectionMouse(0, width-2, first+1, true)
	if got := u.selection.Text(); got != strings.Join(want[:2], "\n") {
		t.Fatalf("restart selected underlying viewport: %q", got)
	}
}

func TestSelectionScrolledClickUsesDocumentTarget(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.ensureShell()
	t.Cleanup(func() { u.shell.diffScreen.Close() })
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "reader"}})
	for i := range 40 {
		text := fmt.Sprintf("prompt %02d", i)
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "turn", "item": map[string]any{
			"id": fmt.Sprintf("command-%d", i), "type": "commandExecution", "command": "echo " + text, "status": "completed", "exitCode": 0,
			"aggregatedOutput": text + strings.Repeat("\nmore output", 20),
		}})
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "turn", "item": map[string]any{
			"id": fmt.Sprintf("answer-%d", i), "type": "agentMessage", "text": fmt.Sprintf("step %d", i),
		}})
	}
	for _, key := range []byte("\x023j") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.paint(&strings.Builder{}, 120, 30); err != nil {
		t.Fatal(err)
	}
	r := u.shell.layout.Main
	x, y := r.X+u.agents.FeedLeft, r.Y+u.agents.FeedTop
	selectionTestDrag(t, u.shell, x, y, x+1, y)
	s := u.shell.selection
	if !s.ScrollKey("\x1b[H") {
		t.Fatal("Home did not scroll frozen selection")
	}
	y = -1
	for row := s.Rect.Y; row < s.Rect.Y+s.Rect.H; row++ {
		at := s.DocumentY(row) - s.Rect.Y
		if at < len(s.questions) && (s.questions[at] != 0 || s.snippets[at] != (activityui.Snippet{})) && strings.Contains(ansi.Strip(s.Rows[s.DocumentY(row)]), "prompt 00") {
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
	s := u.output.Selection
	rect := dialogFrameRect(t, outputDialogScreen(u))
	bottom := rect.Y + rect.H - 1
	for range 50 {
		u.outputMouse(32, rect.X+rect.W-3, bottom, false)
	}
	u.outputMouse(0, rect.X+rect.W-3, bottom, true)
	if got := s.Text(); got != strings.Join(lines, "\n") {
		t.Fatalf("edge drag did not select off-screen text: %q", got)
	}
}
