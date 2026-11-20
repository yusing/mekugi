package launch

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/terminalui"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func navigationBatchPaint(t *testing.T, u *appServerUI, width, height int) *vt.Emulator {
	t.Helper()
	var frame bytes.Buffer
	terminalTest(u).Frame.Rows = nil
	if err := u.testPaint(&frame, width, height); err != nil {
		t.Fatal(err)
	}
	screen := vt.NewEmulator(width, height)
	t.Cleanup(func() { screen.Close() })
	if _, err := screen.Write(frame.Bytes()); err != nil {
		t.Fatal(err)
	}
	return screen
}

func navigationBatchMouse(t *testing.T, u *terminalUI, button, x, y int, release bool) {
	t.Helper()
	ending := "M"
	if release {
		ending = "m"
	}
	if err := u.mouse(fmt.Sprintf("\x1b[<%d;%d;%d%s", button, x+1, y+1, ending)); err != nil {
		t.Fatal(err)
	}
}

func TestNativeNavigationBatchStatusClicks(t *testing.T) {
	for _, width := range []int{16, 30, 52, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.attached.presentation.Status.Text = "Ready"
			terminalTest(u).DiffUnseen = true
			// The roster hint appears only while a subagent exists.
			u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", Responding: true}}})
			screen := navigationBatchPaint(t, u, width, 24)
			footer := strings.Split(screen.String(), "\n")[23]
			// Locate hints in the rendered terminal, not their own hit rectangles.
			for _, target := range []struct {
				hint string
				pane int
			}{{"→ diff", 1}, {"↓ roster", 3}} {
				start := strings.Index(footer, target.hint)
				if start < 0 {
					// Narrow stat lines drop hints before the agent name.
					if width >= 120 {
						t.Fatalf("hint %q missing: %q", target.hint, footer)
					}
					continue
				}
				left := ansi.StringWidth(footer[:start])
				for x := left; x < left+ansi.StringWidth(target.hint); x++ {
					terminalTest(u).Focus = 0
					terminalTest(u).DiffOpen = false
					navigationBatchMouse(t, terminalTest(u), 0, x, 23, false)
					if terminalTest(u).Focus != target.pane {
						t.Fatalf("hint %q at cell %d: focus=%d, footer=%q", target.hint, x, terminalTest(u).Focus, footer)
					}
					if target.pane == 1 && !terminalTest(u).DiffOpen {
						t.Fatalf("hint %q did not open Diff", target.hint)
					}
				}
			}
			// Releases and right clicks must not switch panes.
			terminalTest(u).Focus = 3
			navigationBatchMouse(t, terminalTest(u), 0, 2, 23, true)
			navigationBatchMouse(t, terminalTest(u), 2, 2, 23, false)
			if terminalTest(u).Focus != 3 {
				t.Fatal("non-left-press status event switched panes")
			}
		})
	}
}

func TestNativeNavigationBatchDiffBadgeCells(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.attached.presentation.Status.Text = "Ready"
	terminalTest(u).DiffUnseen = true
	u.testActivity(u.shell.Agents).Coverage = ""
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", Responding: true}}})
	screen := navigationBatchPaint(t, u, 120, 24)
	footer := strings.Split(screen.String(), "\n")[23]
	before, _, ok := strings.Cut(footer, "→ diff ●")
	if !ok {
		t.Fatalf("diff badge must follow its label with a space: %q", footer)
	}
	dot := ansi.StringWidth(before + "→ diff ")
	if screen.CellAt(dot-1, 23).Content != " " || screen.CellAt(dot, 23).Content != "●" || screen.CellAt(dot+1, 23).Content != " " {
		t.Fatal("diff badge does not occupy its own cell between spaces")
	}
	color := vt.NewEmulator(1, 1)
	defer color.Close()
	if _, err := color.Write([]byte(terminalui.Amber + "●")); err != nil {
		t.Fatal(err)
	}
	if screen.CellAt(dot, 23).Style.Fg != color.CellAt(0, 0).Style.Fg {
		t.Fatal("diff badge lost its amber indicator color")
	}
	terminalTest(u).Focus = 0
	navigationBatchMouse(t, terminalTest(u), 0, dot, 23, false)
	if terminalTest(u).Focus != 1 {
		t.Fatal("clicking the diff indicator did not open Diff")
	}
	terminalTest(u).Focus = 0
	screen = navigationBatchPaint(t, u, 120, 24)
	before0, _, ok0 := strings.Cut(strings.Split(screen.String(), "\n")[23], "↓ roster")
	if !ok0 {
		t.Fatalf("roster hint is missing: %q", footer)
	}
	navigationBatchMouse(t, terminalTest(u), 0, ansi.StringWidth(before0), 23, false)
	if terminalTest(u).Focus != 3 {
		t.Fatal("clicking the roster hint did not open the roster")
	}
}

func TestNativeNavigationBatchModalPrecedence(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	// The roster sheet stays open only while a subagent exists.
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	terminalTest(u).Focus = 3
	terminalTest(u).openBlocks(u.shell.Transcript, []activityui.Block{{Kind: "op", Verb: "Read", Path: "file.go", Body: "dialog body"}})
	navigationBatchPaint(t, u, 120, 24)
	// The status row is outside the modal. Its click dismisses the modal,
	// but must not also activate the underlying Main selector.
	navigationBatchMouse(t, terminalTest(u), 0, 2, 23, false)
	if terminalTest(u).Dialog != nil || terminalTest(u).Focus != 3 {
		t.Fatalf("status click leaked through modal: output=%v focus=%d", terminalTest(u).Dialog != nil, terminalTest(u).Focus)
	}
}

func TestNativeNavigationBatchSelectorReleaseEndsDividerDrag(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	terminalTest(u).DiffOpen = true
	navigationBatchPaint(t, u, 120, 24)
	divider := terminalTest(u).Layout.Vertical
	if divider <= 0 {
		t.Fatal("fixture needs a vertical divider")
	}
	navigationBatchMouse(t, terminalTest(u), 0, divider, 2, false)
	if !terminalTest(u).Dragging() {
		t.Fatal("divider press did not start resizing")
	}
	focus := terminalTest(u).Focus
	navigationBatchMouse(t, terminalTest(u), 0, 2, 23, true)
	if terminalTest(u).Dragging() || terminalTest(u).Focus != focus {
		t.Fatal("release over a selector must end resizing without switching panes")
	}
	split := terminalTest(u).Split
	navigationBatchMouse(t, terminalTest(u), 35, divider+5, 2, false)
	if terminalTest(u).Split != split {
		t.Fatal("unpressed pointer movement continued resizing after release")
	}
}

func TestNativeNavigationBatchFilterBinding(t *testing.T) {
	for _, pane := range []int{3} {
		t.Run(fmt.Sprint(pane), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			terminalTest(u).Focus, terminalTest(u).Width, terminalTest(u).Height = pane, 140, 24
			for _, wantOnly := range []bool{true, false} {
				if err := terminalTest(u).key('a'); err != nil {
					t.Fatal(err)
				}
				if u.testActivity(u.shell.Agents).displayingChild() != wantOnly {
					t.Fatalf("a: only=%v, want %v", u.testActivity(u.shell.Agents).displayingChild(), wantOnly)
				}
				wantHint := "a child"
				if wantOnly {
					wantHint = "a main"
				}
				status := ansi.Strip(navigationBatchPaint(t, u, 140, 24).String())
				if !strings.Contains(status, wantHint) || strings.Contains(status, "o only") {
					t.Fatalf("filter hint does not describe the next action: %q", status)
				}
				if err := terminalTest(u).key('o'); err != nil {
					t.Fatal(err)
				}
				if u.testActivity(u.shell.Agents).displayingChild() != wantOnly {
					t.Fatal("retired o binding still toggles filtering")
				}
			}
		})
	}
}

func navigationBatchDialog() *terminalUI {
	v := newActivityAdapter()
	v.Painter.Theme = diffrender.DarkTheme
	var pages []activityui.Block
	retention := new(activitymodel.Retention)
	for i, path := range []string{
		"/workspace/deeply/nested/internal/router/navigation.go",
		"/workspace/deeply/nested/internal/ui/activity/paint.go",
		"/workspace/deeply/nested/internal/路径/日本語.go",
		"/workspace/deeply/nested/internal/ui/diffview/frame.go",
		"/workspace/deeply/nested/internal/router/output.go",
	} {
		output := retention.New()
		output.Write(fmt.Sprintf("page %d only\n", i+1))
		output.Finish(nil, new(0))
		block := testOutputBlock(v, activityui.Block{Kind: "op", Verb: "Read", Path: path}, output)
		if i == 2 {
			block.Path, block.Reads = "", []activitymodel.Read{{Path: path}}
		}
		pages = append(pages, block)
	}
	u := &terminalUI{Attachment: &terminalsession.Attachment{}}
	u.openBlocks(v.bind(u), pages)
	u.Dialog.ShowPage(2)
	return u
}

func navigationBatchDialogFrame(t *testing.T, u *terminalUI, width int) string {
	t.Helper()
	rows := make([]string, 18)
	u.main.detailFiles.prepareOutput(&u.main.attached.session, u.Attachment)
	u.Dialog.Paint(rows, terminalsession.Rect{W: width, H: len(rows)})
	screen := vt.NewEmulator(width, len(rows))
	defer screen.Close()
	if _, err := screen.Write([]byte("\x1b[H" + strings.Join(rows, "\r\n"))); err != nil {
		t.Fatal(err)
	}
	return screen.String()
}

func TestNativeNavigationBatchDialogPathTabs(t *testing.T) {
	for _, width := range []int{56, 86, 120, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u := navigationBatchDialog()
			frame := navigationBatchDialogFrame(t, u, width)
			rows := strings.Split(frame, "\n")
			rect := dialogFrameRect(t, rows)
			row := rows[rect.Y+1]
			visible := 0
			for page, block := range u.Dialog.Pages {
				prefix := fmt.Sprintf(" %d Read", page+1)
				before, _, ok := strings.Cut(row, prefix)
				if !ok {
					continue
				}
				visible++
				path := block.Path
				if path == "" {
					path = block.Reads[0].Path
				}
				if !strings.Contains(row, filepath.Base(path)) {
					t.Fatalf("page %d lost its filename: %q", page, row)
				}
				x := ansi.StringWidth(before) + ansi.StringWidth(prefix)
				navigationBatchMouse(t, u, 0, x, rect.Y+1, false)
				if u.Dialog.Page != page {
					t.Fatalf("rendered tab selected page %d, want %d", u.Dialog.Page, page)
				}
			}
			if visible == 0 {
				t.Fatal("dialog rendered no clickable path tabs")
			}
			navigationBatchDialogFrame(t, u, width)
			if got, want := u.Dialog.Document.Text, fmt.Sprintf("page %d only", u.Dialog.Page+1); got != want {
				t.Fatalf("selected tab showed another command's output: %q, want %q", got, want)
			}
		})
	}
}

func TestUISnapshotNativeNavigationBatchStatus(t *testing.T) {
	for _, width := range []int{16, 30, 52, 120} {
		for _, only := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/only%v", width, only), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, "/workspace")
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.attached.presentation.Status.Text = "Ready"
				terminalTest(u).DiffUnseen = true
				terminalTest(u).Focus = 3
				u.testActivity(u.shell.Agents).setDisplayFilter(only, u.shell.Agents.Selected)
				screen := navigationBatchPaint(t, u, width, 24)
				footer := strings.Split(screen.String(), "\n")[23]
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/native-navigation-status-%d-only%v.txt", width, only), footer+"\n")
			})
		}
	}
}

func TestUISnapshotNativeNavigationBatchDialogTabs(t *testing.T) {
	for _, width := range []int{56, 86, 120, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u := navigationBatchDialog()
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/native-navigation-dialog-%d.txt", width), navigationBatchDialogFrame(t, u, width)+"\n")
		})
	}
}
