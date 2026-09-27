package router

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

func TestTerminalUIDockLingersAfterLastAnimation(t *testing.T) {
	u := &terminalUI{}
	now := time.Now()
	u.mainDock = diffview.PreviewPane{
		Order: []string{"edit"}, Motion: diffview.PreviewMotion{Enabled: true},
		Views: map[string]*diffview.PreviewView{"edit": {Complete: true, Fading: now.Add(time.Second)}},
	}
	u.dockSeen[0] = now.Add(-nativeDockLinger)
	if !u.animating(now) || len(u.mainDock.Order) == 0 {
		t.Fatal("animated completed preview disappeared")
	}
	u.animating(now.Add(time.Second))
	if len(u.mainDock.Order) == 0 {
		t.Fatal("preview closed immediately after animation")
	}
	u.animating(now.Add(nativeDockLinger))
	if len(u.mainDock.Order) != 0 {
		t.Fatal("finished preview did not expire")
	}
}

func TestTerminalUIIncrementalPaint(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.draft = "old draft text"
	screen := vt.NewEmulator(120, 30)
	defer screen.Close()
	var wire bytes.Buffer
	out := io.MultiWriter(screen, &wire)
	if err := u.paint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	wire.Reset()
	if err := u.paint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "\x1b[2K") {
		t.Fatal("unchanged frame erased terminal rows")
	}
	u.draft = "new"
	wire.Reset()
	if err := u.paint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	if cleared := strings.Count(wire.String(), "\x1b[2K"); cleared != 1 {
		t.Fatalf("single-line input edit repainted %d rows", cleared)
	}
	if strings.Contains(screen.String(), "old draft text") || !strings.Contains(screen.String(), "new") {
		t.Fatal("incremental input update retained stale text")
	}
	// Resize and close the auxiliary panes; removed content must be cleared.
	u.shell.side = false
	screen.Resize(80, 20)
	if err := u.paint(screen, 80, 20); err != nil {
		t.Fatal(err)
	}
	fresh, _ := newAppServerTestUI()
	fresh.ensureShell()
	fresh.shell.side, fresh.draft = false, "new"
	want := vt.NewEmulator(80, 20)
	defer want.Close()
	if err := fresh.paint(want, 80, 20); err != nil {
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
		v := newLiveActivityView()
		v.following = false
		v.offset = 50
		v.feedRows = 10
		v.feedLines = 100
		v.handleKey("", key)
		if v.offset != next || v.following != (follow || next >= 90) {
			t.Fatalf("agents %c: %d %v, expected %d %v", key, v.offset, v.following, next, follow)
		}
	}
	v := newLiveActivityView()
	v.feedRows = 10
	v.feedLines = 100
	v.handleMouse('k', 2, 2)
	if v.offset != 87 || v.following {
		t.Fatalf("wheel did not pause/scroll: %+v", v)
	}
	for _, seq := range []string{"\x1b[H", "\x1b[1~", "\x1bOH"} {
		var escape string
		for _, b := range []byte(seq) {
			escape, _ = v.handleKey(escape, b)
		}
		if v.offset != 0 {
			t.Fatalf("Home %q: %d", seq, v.offset)
		}
	}
	var escape string
	for _, b := range []byte("\x1b[F") {
		escape, _ = v.handleKey(escape, b)
	}
	if v.offset != 90 || !v.following {
		t.Fatal("End must scroll to bottom and resume following")
	}

}

func TestTerminalUIIdleLiveLayout(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	screen := vt.NewEmulator(120, 40)
	defer screen.Close()
	paint := func() string {
		t.Helper()
		if err := u.paint(screen, 120, 40); err != nil {
			t.Fatal(err)
		}
		return screen.String()
	}
	workspace := t.TempDir()
	u.shell.preview(projectStockPatchPreview(t.Context(), workspace, diffview.Preview{Workspace: workspace, ID: "main-edit", Caller: "/root", Tool: applyPatchToolName, Status: diffview.PreviewEdit, Input: "*** Begin Patch\n*** Add File: idle-live.txt\n+unique-live-line\n*** End Patch"}))
	u.agents.agents = []activityPaneAgent{{Name: "/root", Responding: true}}
	frame := paint()
	if u.shell.layout.agents.h != 0 || !strings.Contains(frame, "3 Live") || !strings.Contains(frame, "unique-live-line") {
		t.Fatalf("no children must show full Live:\n%s", frame)
	}
	if err := u.shell.mouse("\x1b[<0;70;4M"); err != nil {
		t.Fatal(err)
	}
	if u.shell.focus != 2 {
		t.Fatal("Live click did not focus the right pane")
	}
	frame = paint()
	if strings.Contains(frame, "3 Activity") || strings.Contains(frame, "n/p agent") {
		t.Fatalf("Live advertised hidden Activity controls:\n%s", frame)
	}
	u.agents.agents = append(u.agents.agents, activityPaneAgent{Name: "/root/worker", Final: true})
	frame = paint()
	activity := u.shell.layout.agents
	innerHeight := u.shell.layout.codex.h
	dockRows := max(1, int(float64(innerHeight)*.35+.5))
	if activity.y != 1+dockRows || activity.h != innerHeight-dockRows || !strings.Contains(frame, "LIVE") {
		t.Fatalf("idle child layout: activity=%+v inner=%d\n%s", activity, innerHeight, frame)
	}
	for _, key := range []byte{2, '2'} {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	frame = paint()
	if u.shell.layout.diff != activity || !strings.Contains(frame, "unique-live-line") {
		t.Fatalf("diff replaced live dock:\n%s", frame)
	}
	u.agents.agents[1].Responding = true
	paint()
	if u.shell.layout.diff.y != 1 {
		t.Fatal("active child retained idle Live space")
	}
	u.agents.agents = u.agents.agents[:1]
	frame = paint()
	if u.shell.layout.diff.y <= 1 || !strings.Contains(frame, "LIVE") {
		t.Fatalf("no-child diff replaced live dock:\n%s", frame)
	}
	for _, key := range []byte{2, '3'} {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if frame = paint(); u.shell.layout.agents.h != 0 || !strings.Contains(frame, "3 Live") || !strings.Contains(frame, "unique-live-line") {
		t.Fatalf("Live did not return:\n%s", frame)
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
					u.shell.diffOpen = saved
					if child {
						u.agents.agents = []activityPaneAgent{{Name: "/root/worker", Final: true}}
					}
					screen := vt.NewEmulator(120, 40)
					defer screen.Close()
					paint := func() {
						t.Helper()
						if err := u.paint(screen, 120, 40); err != nil {
							t.Fatal(err)
						}
					}
					assertCollapsed := func() {
						t.Helper()
						paint()
						content := u.shell.layout.agents
						if saved {
							content = u.shell.layout.diff
						}
						if u.shell.layout.live.h != 0 || content.y != 1 || content.h != u.shell.layout.codex.h || strings.Contains(screen.String(), "LIVE") || strings.Contains(screen.String(), "3 Live") {
							t.Fatalf("empty Live area did not collapse: %+v\n%s", u.shell.layout, screen.String())
						}
					}
					assertCollapsed()
					preview := diffview.Preview{ID: "edit", Caller: caller, Tool: applyPatchToolName, Status: diffview.PreviewEdit, Input: "*** Begin Patch\n*** Add File: live.txt\n+live\n*** End Patch", Complete: true}
					preview.Workspace = t.TempDir()
					preview = projectStockPatchPreview(t.Context(), preview.Workspace, preview)
					u.shell.preview(preview)
					now := time.Now()
					u.shell.animating(now.Add(nativeDockLinger - time.Millisecond))
					paint()
					if u.shell.layout.live.h == 0 {
						t.Fatal("Live disappeared before its linger elapsed")
					}
					u.shell.animating(now.Add(nativeDockLinger))
					assertCollapsed()
					preview.Complete = false
					u.shell.preview(preview)
					paint()
					if u.shell.layout.live.h == 0 {
						t.Fatal("new preview did not reopen Live")
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
			u.agents.agents = []activityPaneAgent{{Name: "/root/worker", Responding: true}}
			view, caller := u.view, "/root"
			if activity {
				view, caller = u.agents, "/root/worker"
				view.only, view.selected = true, caller
				u.shell.focus = 2
			}
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: caller, Kind: "text", Text: strings.Repeat("transcript row\n\n", 80) + "LATEST_TRANSCRIPT"}}})
			screen := vt.NewEmulator(120, 40)
			defer screen.Close()
			paint := func(width, height int) string {
				t.Helper()
				screen.Resize(width, height)
				if err := u.paint(screen, width, height); err != nil {
					t.Fatal(err)
				}
				return screen.String()
			}
			assertFollow := func(frame string) {
				t.Helper()
				if !view.following || view.offset != max(0, view.feedLines-view.feedRows) || !strings.Contains(frame, "LATEST_TRANSCRIPT") || strings.Contains(frame, "Back to bottom") {
					t.Fatalf("lost following:\n%s", frame)
				}
			}
			assertFollow(paint(120, 40))
			workspace := t.TempDir()
			u.shell.preview(projectStockPatchPreview(t.Context(), workspace, diffview.Preview{ID: "edit", Workspace: workspace, Caller: caller, Tool: applyPatchToolName, Status: diffview.PreviewEdit, Input: "*** Begin Patch\n*** Add File: follow.txt\n+live content\n*** End Patch"}))
			assertFollow(paint(120, 40))
			u.agents.agents[0].Responding = false
			assertFollow(paint(120, 40))
			u.shell.diffOpen = true
			paint(120, 40)
			if !view.following {
				t.Fatal("opening Diff paused transcript")
			}
			u.shell.diffOpen = false
			assertFollow(paint(140, 32))
			view.scrollKey(terminalui.PaneWheelDown)
			assertFollow(paint(140, 32))
			view.scrollKey(terminalui.PaneWheelUp)
			frame := paint(140, 32)
			if view.following || !strings.Contains(frame, "↓ Back to bottom · esc") {
				t.Fatalf("missing manual scrollback hint:\n%s", frame)
			}
			offset := view.offset
			if activity {
				if err := u.shell.key('r'); err != nil {
					t.Fatal(err)
				}
				if view.following || view.offset != offset {
					t.Fatal("r resumed Activity follow")
				}
				if strings.Contains(frame, "r follow") {
					t.Fatal("Activity still advertises r follow")
				}
			}
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: caller, Kind: "text", Text: "LATEST_TRANSCRIPT new"}}})
			paint(140, 32)
			if view.following || view.offset != offset {
				t.Fatal("new output moved manual scrollback")
			}
			if err := u.shell.key(27); err != nil {
				t.Fatal(err)
			}
			u.shell.sequenceAt = time.Now().Add(-time.Second)
			if err := u.shell.flushEscape(); err != nil {
				t.Fatal(err)
			}
			assertFollow(paint(140, 32))
			if view.unseen != 0 {
				t.Fatal("Escape retained unseen count")
			}
			view.scrollKey('b')
			paint(140, 32)
			for !view.following {
				view.scrollKey(terminalui.PaneWheelDown)
			}
			assertFollow(paint(140, 32))
			view.toggleSnippet(liveActivitySnippet{1, 0})
			if !view.following {
				t.Fatal("expanding a snippet paused follow")
			}
		})
	}
}
