package router

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/vt"
)

// Existing layout fixtures exercise an established stream, after admission.
func revealNativeDock(u *terminalUI) {
	u.liveDock.Tick(u.main.now().Add(previewview.RevealDelay))
}

func TestUISnapshotNativeDockReveal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u := nativeBatchFixture(t)
		now := u.now()
		u.clock = func() time.Time { return now }
		screen := vt.NewEmulator(100, 28)
		defer screen.Close()
		paint := func(name string) {
			t.Helper()
			if err := u.testPaint(screen, 100, 28); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n"))
		}
		p := nativeBatchPreview("small", "/root", "small")
		u.preview(p, false)
		now = now.Add(previewview.RevealDelay - time.Millisecond)
		u.shell.liveDock.Tick(now)
		paint("native-dock-before-reveal")
		p.Complete = true
		u.preview(p, false)
		now = now.Add(previewview.RevealDelay)
		if u.shell.liveDock.Tick(now) || len(u.shell.liveDock.Order) != 0 {
			t.Fatal("short edit opened a dock after completion")
		}
		paint("native-dock-short-completed")
		p.ID, p.Complete = "stream", false
		u.preview(p, false)
		now = now.Add(previewview.RevealDelay)
		u.shell.liveDock.Tick(now)
		paint("native-dock-stream-revealed")
		p.Complete = true
		u.preview(p, false)
		now = now.Add(previewview.CompletionHold)
		u.shell.liveDock.Tick(now)
		paint("native-dock-stream-settled")
	})
}

func TestNativeDockPendingLifecycle(t *testing.T) {
	for _, finish := range []string{"completed", "withdrawn", "turn ended", "reconnect"} {
		t.Run(finish, func(t *testing.T) {
			u := nativeBatchFixture(t)
			p := nativeBatchPreview("main", "/root", "main")
			p.Thread, p.Turn = "main", "turn"
			u.preview(p, false)
			child := nativeBatchPreview("child", "/root/editor", "child")
			child.Thread, child.Turn = "child", "turn"
			u.preview(child, false)
			switch finish {
			case "completed":
				u.preview(previewmodel.Preview{ID: p.ID}, false)
			case "withdrawn":
				u.preview(previewmodel.Preview{ID: p.ID, Workspace: p.Workspace}, false)
			case "turn ended":
				u.endTurnPreviews("main", "turn")
			case "reconnect":
				u.applyReview(t.Context(), sessionreview.Event{Kind: "coverage", Status: "RECONNECTING: test"})
			}
			revealNativeDock(u.shell)
			if u.shell.liveDock.Views[p.ID] != nil {
				t.Fatal("retired pending preview reappeared")
			}
			if finish != "reconnect" && u.shell.liveDock.Views[child.ID] == nil {
				t.Fatal("another caller's pending preview was removed")
			}
		})
	}
}
