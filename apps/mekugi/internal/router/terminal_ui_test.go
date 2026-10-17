package router

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/vt"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

func TestTerminalUIBatchKeepsSettlingTime(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	p := previewmodel.Preview{ID: "edit", Caller: "/root", Workspace: t.TempDir(), Status: previewmodel.PreviewEdit, Input: "source"}
	u.shell.preview(p, false)
	revealNativeDock(u.shell)
	p.Complete = true
	u.shell.preview(p, false)
	revealNativeDock(u.shell)
	now := time.Now()
	if !u.shell.animating(now) || len(u.shell.liveDock.Order) != 1 {
		t.Fatal("completed batch did not wait for another edit")
	}
	u.shell.animating(now.Add(nativeDockMinimum + time.Millisecond))
	if len(u.shell.liveDock.Order) != 0 {
		t.Fatal("settled batch did not close")
	}
}

func TestTerminalUIIncrementalPaint(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "old draft text"
	screen := vt.NewEmulator(120, 30)
	defer screen.Close()
	var wire bytes.Buffer
	out := io.MultiWriter(screen, &wire)
	if err := u.testPaint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	wire.Reset()
	if err := u.testPaint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "\x1b[2K") {
		t.Fatal("unchanged frame erased terminal rows")
	}
	u.Text = "new"
	wire.Reset()
	if err := u.testPaint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	if cleared := strings.Count(wire.String(), "\x1b[2K"); cleared != 1 {
		t.Fatalf("single-line input edit repainted %d rows", cleared)
	}
	if strings.Contains(screen.String(), "old draft text") || !strings.Contains(screen.String(), "new") {
		t.Fatal("incremental input update retained stale text")
	}
	// Resize and close the auxiliary panes; removed content must be cleared.
	screen.Resize(80, 20)
	if err := u.testPaint(screen, 80, 20); err != nil {
		t.Fatal(err)
	}
	fresh, _ := newAppServerTestUI()
	fresh.ensureShell()
	fresh.Text = "new"
	want := vt.NewEmulator(80, 20)
	defer want.Close()
	if err := fresh.testPaint(want, 80, 20); err != nil {
		t.Fatal(err)
	}
	if screen.String() != want.String() {
		t.Fatal("incremental resize differs from a fresh terminal frame")
	}
}

func TestPaneScrollUnified(t *testing.T) {
	for _, key := range []byte{'j', 'k', ' ', 'b', 'g', 'G'} {
		next, follow, ok := terminalui.PaneScroll(key, 50, 10, 100)
		if !ok {
			t.Fatal("missing scroll binding")
		}
		v := newActivityAdapter()
		v.Following = false
		v.Offset = 50
		v.FeedRows = 10
		v.FeedLines = 100
		v.ScrollKey(key)
		if v.Offset != next || v.Following != (follow || next >= 90) {
			t.Fatalf("agents %c: %d %v, expected %d %v", key, v.Offset, v.Following, next, follow)
		}
	}
	v := newActivityAdapter()
	v.FeedRows = 10
	v.FeedLines = 100
	v.testHandleMouse('k', 2, 2)
	if v.Offset != 87 || v.Following {
		t.Fatalf("wheel did not pause/scroll: %+v", v)
	}

}

func TestTerminalUIIdleLiveCollapsesAfterLinger(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, saved := range []bool{false, true} {
			for _, caller := range []string{"/root", "/root/worker"} {
				t.Run(fmt.Sprintf("child=%t/diff=%t/caller=%s", child, saved, caller), func(t *testing.T) {
					u, _ := newAppServerTestUI()
					u.ensureShell()
					defer u.shell.diffScreen.Close()
					u.shell.DiffOpen = saved

					if child {
						u.agents.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Final: true}}
					}
					screen := vt.NewEmulator(120, 40)
					defer screen.Close()
					paint := func() {
						t.Helper()
						if err := u.testPaint(screen, 120, 40); err != nil {
							t.Fatal(err)
						}
					}
					assertCollapsed := func() {
						t.Helper()
						paint()
						content := u.shell.Layout.Main
						if saved {
							content = u.shell.Layout.Diff
						}
						if u.shell.Layout.Live.H != 0 || content.Y != 1 || content.H != u.shell.Layout.Main.H || strings.Contains(screen.String(), "LIVE") || strings.Contains(screen.String(), "3 Live") {
							t.Fatalf("empty Live area did not collapse: %+v\n%s", u.shell.Layout, screen.String())
						}
					}
					assertCollapsed()
					preview := previewmodel.Preview{ID: "edit", Caller: caller, Tool: applyPatchToolName, Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: live.txt\n+live\n*** End Patch"}
					preview.Workspace = t.TempDir()
					preview = projectStockPatchPreview(t.Context(), preview.Workspace, preview)
					u.shell.preview(preview, false)
					revealNativeDock(u.shell)
					preview.Complete = true
					u.shell.preview(preview, false)
					now := time.Now()
					u.shell.animating(now.Add(nativeDockMinimum - time.Millisecond))
					paint()
					if len(u.shell.liveDock.Order) != 1 || strings.Contains(screen.String(), "LIVE ·") != (caller == "/root") {
						t.Fatal("retained Live batch did not respect the owning pane")
					}
					u.shell.animating(now.Add(nativeDockMinimum))
					assertCollapsed()
					preview.Complete = false
					u.shell.preview(preview, false)
					revealNativeDock(u.shell)
					paint()
					if len(u.shell.liveDock.Order) != 1 || strings.Contains(screen.String(), "LIVE ·") != (caller == "/root") {
						t.Fatal("new batch did not respect the owning pane")
					}
				})
			}
		}
	}
}

func TestTerminalUITranscriptFollowAcrossLiveLayout(t *testing.T) {
	for _, activity := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "activity"}[activity], func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			u.agents.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Responding: true}}
			view, caller := u.view, "/root"
			if activity {
				view, caller = u.agents, "/root/worker"
				view.setDisplayFilter(true, caller)
				u.shell.Focus = 0
			}
			for i := range 80 {
				text := "transcript row"
				if i == 79 {
					text = "LATEST_TRANSCRIPT"
				}
				view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: caller, Kind: "text", Text: text}}})
			}
			screen := vt.NewEmulator(120, 40)
			defer screen.Close()
			paint := func(width, height int) string {
				t.Helper()
				screen.Resize(width, height)
				if err := u.testPaint(screen, width, height); err != nil {
					t.Fatal(err)
				}
				return screen.String()
			}
			assertFollow := func(frame string) {
				t.Helper()
				if !view.Following || view.Offset != max(0, view.FeedLines-view.FeedRows) || !strings.Contains(frame, "LATEST_TRANSCRIPT") || strings.Contains(frame, "Back to bottom") {
					t.Fatalf("lost following:\n%s", frame)
				}
			}
			assertFollow(paint(120, 40))
			workspace := t.TempDir()
			u.shell.preview(projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{ID: "edit", Workspace: workspace, Caller: caller, Tool: applyPatchToolName, Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: follow.txt\n+live content\n*** End Patch"}), false)
			revealNativeDock(u.shell)
			assertLiveFollow := func(frame string) {
				t.Helper()
				body := strings.Join(strings.Split(frame, "\n")[:u.shell.Layout.Roster.Y], "\n")
				if !view.Following || strings.Contains(body, "LATEST_TRANSCRIPT") {
					t.Fatalf("live replacement lost follow or retained transcript:\n%s", frame)
				}
			}
			assertLiveFollow(paint(120, 40))
			u.agents.Agents[0].Responding = false
			assertLiveFollow(paint(120, 40))
			u.shell.DiffOpen = true
			paint(120, 40)
			if !view.Following {
				t.Fatal("opening Diff paused transcript")
			}
			u.shell.DiffOpen = false
			assertLiveFollow(paint(140, 32))
			u.shell.preview(previewmodel.Preview{ID: "edit", Workspace: workspace}, false)
			revealNativeDock(u.shell)
			assertFollow(paint(140, 32))
			view.ScrollKey(terminalui.PaneWheelDown)
			assertFollow(paint(140, 32))
			view.ScrollKey(terminalui.PaneWheelUp)
			frame := paint(140, 32)
			if view.Following || !strings.Contains(frame, "↓ Back to bottom · esc") {
				t.Fatalf("missing manual scrollback hint:\n%s", frame)
			}
			offset := view.Offset
			if activity {
				if err := u.shell.key('r'); err != nil {
					t.Fatal(err)
				}
				if view.Following || view.Offset != offset {
					t.Fatal("r resumed Activity follow")
				}
				if strings.Contains(frame, "r follow") {
					t.Fatal("Activity still advertises r follow")
				}
			}
			view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 2, Agent: caller, Kind: "text", Text: "LATEST_TRANSCRIPT new"}}})
			paint(140, 32)
			if view.Following || view.Offset != offset {
				t.Fatal("new output moved manual scrollback")
			}
			if err := u.shell.key(27); err != nil {
				t.Fatal(err)
			}
			ageTerminalEscape(u.shell)
			if err := u.shell.flushEscape(); err != nil {
				t.Fatal(err)
			}
			assertFollow(paint(140, 32))
			if view.Unseen != 0 {
				t.Fatal("Escape retained unseen count")
			}
			view.ScrollKey('b')
			paint(140, 32)
			for !view.Following {
				view.ScrollKey(terminalui.PaneWheelDown)
			}
			assertFollow(paint(140, 32))
			u.shell.openEntry(view, 1)
			if !view.Following {
				t.Fatal("opening a snippet paused follow")
			}
		})
	}
}

func (u *terminalUI) nativeStatus() string {
	return u.Shell.Status(u.shellStatus(), u.width, u.height)
}
