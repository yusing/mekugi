package router

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/vt"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

func TestTerminalUIBatchKeepsSettlingTime(t *testing.T) {
	u, _ := newAppServerTestUI()

	defer terminalTest(u).Diff.Close()
	p := previewmodel.Preview{ID: "edit", Caller: "/root", Workspace: t.TempDir(), Status: previewmodel.PreviewEdit, Input: "source"}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), p, false)
	revealNativeDock(terminalTest(u))
	p.Complete = true
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), p, false)
	revealNativeDock(terminalTest(u))
	now := time.Now()
	if !terminalTest(u).Live.Tick(now) || len(terminalTest(u).Live.Order) != 1 {
		t.Fatal("completed batch did not wait for another edit")
	}
	terminalTest(u).Live.Tick(now.Add(previewview.CompletionHold + time.Millisecond))
	if len(terminalTest(u).Live.Order) != 0 {
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

					defer terminalTest(u).Diff.Close()
					terminalTest(u).DiffOpen = saved

					if child {
						u.attached.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Final: true}}
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
						content := terminalTest(u).Layout.Main
						if saved {
							content = terminalTest(u).Layout.Diff
						}
						if terminalTest(u).Layout.Live.H != 0 || content.Y != 1 || content.H != terminalTest(u).Layout.Main.H || strings.Contains(screen.String(), "LIVE") || strings.Contains(screen.String(), "3 Live") {
							t.Fatalf("empty Live area did not collapse: %+v\n%s", terminalTest(u).Layout, screen.String())
						}
					}
					assertCollapsed()
					preview := previewmodel.Preview{ID: "edit", Caller: caller, Tool: applyPatchToolName, Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: live.txt\n+live\n*** End Patch"}
					preview.Workspace = t.TempDir()
					preview = projectStockPatchPreview(t.Context(), preview.Workspace, preview)
					applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), preview, false)
					revealNativeDock(terminalTest(u))
					preview.Complete = true
					applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), preview, false)
					now := time.Now()
					terminalTest(u).Live.Tick(now.Add(previewview.CompletionHold - time.Millisecond))
					paint()
					if len(terminalTest(u).Live.Order) != 1 || strings.Contains(screen.String(), "LIVE ·") != (caller == "/root") {
						t.Fatal("retained Live batch did not respect the owning pane")
					}
					terminalTest(u).Live.Tick(now.Add(previewview.CompletionHold))
					assertCollapsed()
					preview.Complete = false
					applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), preview, false)
					revealNativeDock(terminalTest(u))
					paint()
					if len(terminalTest(u).Live.Order) != 1 || strings.Contains(screen.String(), "LIVE ·") != (caller == "/root") {
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

			defer terminalTest(u).Diff.Close()
			u.attached.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/worker", Responding: true}}
			view, caller := u.testActivity(u.shell.Transcript), "/root"
			if activity {
				view, caller = u.testActivity(u.shell.Agents), "/root/worker"
				view.setDisplayFilter(true, caller)
				terminalTest(u).Focus = 0
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
			applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{ID: "edit", Workspace: workspace, Caller: caller, Tool: applyPatchToolName, Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: follow.txt\n+live content\n*** End Patch"}), false)
			revealNativeDock(terminalTest(u))
			assertLiveFollow := func(frame string) {
				t.Helper()
				body := strings.Join(strings.Split(frame, "\n")[:terminalTest(u).Layout.Roster.Y], "\n")
				if !view.Following || strings.Contains(body, "LATEST_TRANSCRIPT") {
					t.Fatalf("live replacement lost follow or retained transcript:\n%s", frame)
				}
			}
			assertLiveFollow(paint(120, 40))
			u.attached.childSource.Agents[0].Responding = false
			assertLiveFollow(paint(120, 40))
			terminalTest(u).DiffOpen = true
			paint(120, 40)
			if !view.Following {
				t.Fatal("opening Diff paused transcript")
			}
			terminalTest(u).DiffOpen = false
			assertLiveFollow(paint(140, 32))
			applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "edit", Workspace: workspace}, false)
			revealNativeDock(terminalTest(u))
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
				if err := terminalTest(u).key('r'); err != nil {
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
			if err := terminalTest(u).key(27); err != nil {
				t.Fatal(err)
			}
			ageTerminalEscape(terminalTest(u))
			if err := terminalTest(u).flushEscape(); err != nil {
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
			terminalTest(u).openEntry(view.View, 1)
			if !view.Following {
				t.Fatal("opening a snippet paused follow")
			}
		})
	}
}

func (u *terminalUI) nativeStatus() string {
	return u.Shell.Status(u.frameInput().Status(), u.Width, u.Height)
}
