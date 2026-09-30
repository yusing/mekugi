package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func navigationBatchPaint(t *testing.T, u *appServerUI, width, height int) *vt.Emulator {
	t.Helper()
	var frame bytes.Buffer
	u.shell.paintedRows = nil
	if err := u.paint(&frame, width, height); err != nil {
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
			u.status = "Ready"
			u.shell.diffUnseen = true
			screen := navigationBatchPaint(t, u, width, 24)
			footer := strings.Split(screen.String(), "\n")[23]
			// Locate selectors in the rendered terminal, not their own hit rectangles.
			for _, target := range []struct {
				prefix string
				pane   int
			}{{" 1 Main", 0}, {" 2 Diff", 1}, {" 3 Activity", 2}, {" 4 Agents", 3}, {" 5 Journal", 4}} {
				marker := target.prefix
				// A narrow footer may show only the beginning of a selector.
				start := strings.Index(footer, marker[:3])
				if start < 0 {
					continue
				}
				left := ansi.StringWidth(footer[:start])
				right := min(width, left+ansi.StringWidth(marker))
				for x := left; x < right; x++ {
					u.shell.focus = (target.pane + 1) % 5
					u.shell.prefix = true
					navigationBatchMouse(t, u.shell, 0, x, 23, false)
					if u.shell.focus != target.pane || u.shell.prefix {
						t.Fatalf("selector %q at cell %d: focus=%d prefix=%v, footer=%q", target.prefix, x, u.shell.focus, u.shell.prefix, footer)
					}
					if target.pane == 1 && (!u.shell.diffOpen || u.shell.journalOpen) || target.pane == 2 && (u.shell.diffOpen || u.shell.journalOpen || !u.shell.activityOpen) || target.pane == 4 && (!u.shell.journalOpen || u.shell.diffOpen) {
						t.Fatalf("selector %q did not open its pane", target.prefix)
					}
				}
			}
			// Releases and right clicks must not switch panes.
			u.shell.focus = 3
			navigationBatchMouse(t, u.shell, 0, 2, 23, true)
			navigationBatchMouse(t, u.shell, 2, 2, 23, false)
			if u.shell.focus != 3 {
				t.Fatal("non-left-press status event switched panes")
			}
		})
	}
}

func TestNativeNavigationBatchDiffBadgeCells(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.status = "Ready"
	u.shell.diffUnseen = true
	u.agents.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/worker", Responding: true}}})
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
	if _, err := color.Write([]byte(activityui.Amber + "●")); err != nil {
		t.Fatal(err)
	}
	if screen.CellAt(dot, 23).Style.Fg != color.CellAt(0, 0).Style.Fg {
		t.Fatal("diff badge lost its amber indicator color")
	}
	u.shell.focus = 0
	navigationBatchMouse(t, u.shell, 0, dot, 23, false)
	if u.shell.focus != 1 {
		t.Fatal("clicking the diff indicator did not open Diff")
	}
	agents := strings.Index(footer, "4 Agents¹")
	if agents < 0 {
		t.Fatalf("responding count is not attached to Agents: %q", footer)
	}
	count := ansi.StringWidth(footer[:agents] + "4 Agents")
	navigationBatchMouse(t, u.shell, 0, count, 23, false)
	if u.shell.focus != 3 {
		t.Fatal("clicking the responding count did not focus Agents")
	}
}

func TestNativeNavigationBatchModalPrecedence(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.focus = 3
	u.shell.openBlocks(u.view, []activityui.Block{{Kind: "op", Verb: "Read", Path: "file.go", Body: "dialog body"}})
	navigationBatchPaint(t, u, 120, 24)
	// The status row is outside the modal. Its click dismisses the modal,
	// but must not also activate the underlying Main selector.
	navigationBatchMouse(t, u.shell, 0, 2, 23, false)
	if u.shell.output != nil || u.shell.focus != 3 {
		t.Fatalf("status click leaked through modal: output=%v focus=%d", u.shell.output != nil, u.shell.focus)
	}
}

func TestNativeNavigationBatchSelectorReleaseEndsDividerDrag(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	navigationBatchPaint(t, u, 120, 24)
	divider := u.shell.layout.vertical
	if divider <= 0 {
		t.Fatal("fixture needs a vertical divider")
	}
	navigationBatchMouse(t, u.shell, 0, divider, 2, false)
	if u.shell.drag == 0 {
		t.Fatal("divider press did not start resizing")
	}
	focus := u.shell.focus
	navigationBatchMouse(t, u.shell, 0, 2, 23, true)
	if u.shell.drag != 0 || u.shell.focus != focus {
		t.Fatal("release over a selector must end resizing without switching panes")
	}
	split := u.shell.split
	navigationBatchMouse(t, u.shell, 35, divider+5, 2, false)
	if u.shell.split != split {
		t.Fatal("unpressed pointer movement continued resizing after release")
	}
}

func TestNativeNavigationBatchFilterBinding(t *testing.T) {
	for _, pane := range []int{2, 3} {
		t.Run(fmt.Sprint(pane), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.shell.focus, u.shell.width, u.shell.height = pane, 140, 24
			for _, wantOnly := range []bool{true, false} {
				if err := u.shell.key('a'); err != nil {
					t.Fatal(err)
				}
				if u.agents.only != wantOnly {
					t.Fatalf("a: only=%v, want %v", u.agents.only, wantOnly)
				}
				wantHint := "a only"
				if wantOnly {
					wantHint = "a all"
				}
				status := ansi.Strip(u.shell.nativeStatus())
				if !strings.Contains(status, wantHint) || strings.Contains(status, "o only") {
					t.Fatalf("filter hint does not describe the next action: %q", status)
				}
				if err := u.shell.key('o'); err != nil {
					t.Fatal(err)
				}
				if u.agents.only != wantOnly {
					t.Fatal("retired o binding still toggles filtering")
				}
			}
		})
	}
}

func navigationBatchDialog() *terminalUI {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	d := &outputDialog{view: v, match: -1}
	retention := new(activityui.Retention)
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
		block := activityui.Block{Kind: "op", Verb: "Read", Path: path, Output: output}
		if i == 2 {
			block.Path, block.Reads = "", []activityui.Read{{Path: path}}
		}
		d.pages = append(d.pages, block)
	}
	d.showPage(2)
	return &terminalUI{output: d}
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
			row := strings.Split(frame, "\n")[u.output.rect.y+2]
			tabs := append([]outputTab(nil), u.output.tabs...)
			for _, tab := range tabs {
				block := u.output.pages[tab.page]
				path := block.Path
				if path == "" {
					path = block.Reads[0].Path
				}
				prefix := fmt.Sprintf(" %d Read", tab.page+1)
				room := tab.to - tab.from - ansi.StringWidth(prefix) - 2
				want := activityui.TruncatePath(path, room)
				if !strings.Contains(row, want) {
					t.Fatalf("page %d did not reuse front path truncation %q: %q", tab.page, want, row)
				}
				// Locate each tab in rendered cells, including the Unicode path.
				index := strings.Index(row, prefix)
				if index < 0 {
					t.Fatalf("page %d label missing: %q", tab.page, row)
				}
				x := ansi.StringWidth(row[:index]) + ansi.StringWidth(prefix)
				navigationBatchMouse(t, u, 0, x, u.output.rect.y+2, false)
				if u.output.page != tab.page {
					t.Fatalf("rendered tab selected page %d, want %d", u.output.page, tab.page)
				}
				// Do not repaint until all original visible hit targets were checked.
			}
			navigationBatchDialogFrame(t, u, width)
			if got, want := u.output.laid.Text, fmt.Sprintf("page %d only", u.output.page+1); got != want {
				t.Fatalf("selected tab showed another command's output: %q, want %q", got, want)
			}
		})
	}
}

func TestNativeNavigationBatchDialogTabRowNarrowPathBudget(t *testing.T) {
	u := navigationBatchDialog()
	row := ansi.Strip(u.output.tabRow(120))
	if len(u.output.tabs) != 5 {
		t.Fatalf("120-column tabs: got %d visible pages, want 5", len(u.output.tabs))
	}
	for _, tab := range u.output.tabs {
		block := u.output.pages[tab.page]
		path := block.Path
		if path == "" {
			path = block.Reads[0].Path
		}
		prefix := fmt.Sprintf(" %d Read", tab.page+1)
		room := tab.to - tab.from - ansi.StringWidth(prefix) - 2
		if room != 15 {
			t.Fatalf("test must exercise the sub-16-column path budget, got %d", room)
		}
		cell := ansi.Cut(row, tab.from, tab.to)
		if want := activityui.TruncatePath(path, room); !strings.Contains(cell, want) {
			t.Fatalf("narrow tab dropped its filename: want %q in %q", want, cell)
		}
	}
}

func TestUISnapshotNativeNavigationBatchStatus(t *testing.T) {
	for _, width := range []int{16, 30, 52, 120} {
		for _, only := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/only%v", width, only), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				u.status = "Ready"
				u.shell.diffUnseen = true
				u.shell.focus, u.agents.only = 3, only
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
