package router

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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
	u.shell.frame.Rows = nil
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
			u.sessionPresentation().Status.Text = "Ready"
			u.shell.diffUnseen = true
			screen := navigationBatchPaint(t, u, width, 24)
			footer := strings.Split(screen.String(), "\n")[23]
			// Locate selectors in the rendered terminal, not their own hit rectangles.
			for _, target := range []struct {
				prefix string
				pane   int
			}{{" 1 Main", 0}, {" 2 Diff", 1}, {" 3 Agents", 3}} {
				marker := target.prefix
				// A narrow footer may show only the beginning of a selector.
				start := strings.Index(footer, marker[:3])
				if start < 0 {
					continue
				}
				left := ansi.StringWidth(footer[:start])
				right := min(width, left+ansi.StringWidth(marker))
				for x := left; x < right; x++ {
					u.shell.Focus = (target.pane + 1) % 4
					u.shell.DiffOpen = false
					u.shell.prefix = true
					navigationBatchMouse(t, u.shell, 0, x, 23, false)
					if u.shell.Focus != target.pane || u.shell.prefix {
						t.Fatalf("selector %q at cell %d: focus=%d prefix=%v, footer=%q", target.prefix, x, u.shell.Focus, u.shell.prefix, footer)
					}
					if target.pane == 1 && !u.shell.DiffOpen || target.pane == 2 && u.shell.DiffOpen {
						t.Fatalf("selector %q did not open its pane", target.prefix)
					}
				}
			}
			// Releases and right clicks must not switch panes.
			u.shell.Focus = 3
			navigationBatchMouse(t, u.shell, 0, 2, 23, true)
			navigationBatchMouse(t, u.shell, 2, 2, 23, false)
			if u.shell.Focus != 3 {
				t.Fatal("non-left-press status event switched panes")
			}
		})
	}
}

func TestNativeNavigationBatchDiffBadgeCells(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.sessionPresentation().Status.Text = "Ready"
	u.shell.diffUnseen = true
	u.testActivity(u.agents).Apply(sessionpresentation.ActivityEvent{Kind: "snapshot", Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", Responding: true}}})
	screen := navigationBatchPaint(t, u, 120, 24)
	footer := strings.Split(screen.String(), "\n")[23]
	start := strings.Index(footer, "2 Diff ●")
	if start < 0 {
		t.Fatalf("diff badge must follow its label with a space: %q", footer)
	}
	dot := ansi.StringWidth(footer[:start] + "2 Diff ")
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
	u.shell.Focus = 0
	navigationBatchMouse(t, u.shell, 0, dot, 23, false)
	if u.shell.Focus != 1 {
		t.Fatal("clicking the diff indicator did not open Diff")
	}
	agents := strings.Index(footer, "3 Agents¹")
	if agents < 0 {
		t.Fatalf("responding count is not attached to Agents: %q", footer)
	}
	count := ansi.StringWidth(footer[:agents] + "3 Agents")
	navigationBatchMouse(t, u.shell, 0, count, 23, false)
	if u.shell.Focus != 3 {
		t.Fatal("clicking the responding count did not focus Agents")
	}
}

func TestNativeNavigationBatchModalPrecedence(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Focus = 3
	u.shell.openBlocks(u.view, []activityui.Block{{Kind: "op", Verb: "Read", Path: "file.go", Body: "dialog body"}})
	navigationBatchPaint(t, u, 120, 24)
	// The status row is outside the modal. Its click dismisses the modal,
	// but must not also activate the underlying Main selector.
	navigationBatchMouse(t, u.shell, 0, 2, 23, false)
	if u.shell.output != nil || u.shell.Focus != 3 {
		t.Fatalf("status click leaked through modal: output=%v focus=%d", u.shell.output != nil, u.shell.Focus)
	}
}

func TestNativeNavigationBatchSelectorReleaseEndsDividerDrag(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.DiffOpen = true
	navigationBatchPaint(t, u, 120, 24)
	divider := u.shell.Layout.Vertical
	if divider <= 0 {
		t.Fatal("fixture needs a vertical divider")
	}
	navigationBatchMouse(t, u.shell, 0, divider, 2, false)
	if !u.shell.Dragging() {
		t.Fatal("divider press did not start resizing")
	}
	focus := u.shell.Focus
	navigationBatchMouse(t, u.shell, 0, 2, 23, true)
	if u.shell.Dragging() || u.shell.Focus != focus {
		t.Fatal("release over a selector must end resizing without switching panes")
	}
	split := u.shell.Split
	navigationBatchMouse(t, u.shell, 35, divider+5, 2, false)
	if u.shell.Split != split {
		t.Fatal("unpressed pointer movement continued resizing after release")
	}
}

func TestNativeNavigationBatchFilterBinding(t *testing.T) {
	for _, pane := range []int{3} {
		t.Run(fmt.Sprint(pane), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.shell.Focus, u.shell.width, u.shell.height = pane, 140, 24
			for _, wantOnly := range []bool{true, false} {
				if err := u.shell.key('a'); err != nil {
					t.Fatal(err)
				}
				if u.testActivity(u.agents).displayingChild() != wantOnly {
					t.Fatalf("a: only=%v, want %v", u.testActivity(u.agents).displayingChild(), wantOnly)
				}
				wantHint := "a child"
				if wantOnly {
					wantHint = "a Main"
				}
				status := ansi.Strip(u.shell.nativeStatus())
				if !strings.Contains(status, wantHint) || strings.Contains(status, "o only") {
					t.Fatalf("filter hint does not describe the next action: %q", status)
				}
				if err := u.shell.key('o'); err != nil {
					t.Fatal(err)
				}
				if u.testActivity(u.agents).displayingChild() != wantOnly {
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
	u := &terminalUI{}
	u.openBlocks(v.bind(u), pages)
	u.output.ShowPage(2)
	return u
}

func navigationBatchDialogFrame(t *testing.T, u *terminalUI, width int) string {
	t.Helper()
	rows := make([]string, 18)
	u.paintOutput(rows, width, len(rows))
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
			row := rows[rect.Y+2]
			visible := 0
			for page, block := range u.output.pages {
				prefix := fmt.Sprintf(" %d Read", page+1)
				index := strings.Index(row, prefix)
				if index < 0 {
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
				x := ansi.StringWidth(row[:index]) + ansi.StringWidth(prefix)
				navigationBatchMouse(t, u, 0, x, rect.Y+2, false)
				if u.output.Page != page {
					t.Fatalf("rendered tab selected page %d, want %d", u.output.Page, page)
				}
			}
			if visible == 0 {
				t.Fatal("dialog rendered no clickable path tabs")
			}
			navigationBatchDialogFrame(t, u, width)
			if got, want := u.output.Document.Text, fmt.Sprintf("page %d only", u.output.Page+1); got != want {
				t.Fatalf("selected tab showed another command's output: %q, want %q", got, want)
			}
		})
	}
}

func TestUISnapshotNativeNavigationBatchStatus(t *testing.T) {
	for _, width := range []int{16, 30, 52, 120} {
		for _, only := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/only%v", width, only), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.sessionPresentation().Status.Text = "Ready"
				u.shell.diffUnseen = true
				u.shell.Focus = 3
				u.testActivity(u.agents).setDisplayFilter(only, u.agents.Selected)
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
