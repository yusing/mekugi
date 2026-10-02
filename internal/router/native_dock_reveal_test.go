package router

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// Existing layout fixtures exercise an established stream, after admission.
func revealNativeDock(u *terminalUI) {
	u.animating(time.Now().Add(nativeDockReveal))
}

func TestUISnapshotNativeDockReveal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u := nativeBatchFixture(t)
		screen := vt.NewEmulator(100, 28)
		defer screen.Close()
		paint := func(name string) {
			t.Helper()
			if err := u.paint(screen, 100, 28); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n"))
		}
		p := nativeBatchPreview("small", "/root", "small")
		u.shell.preview(p)
		time.Sleep(nativeDockReveal - time.Millisecond)
		u.shell.animating(time.Now())
		paint("native-dock-before-reveal")
		p.Complete = true
		u.shell.preview(p)
		time.Sleep(nativeDockReveal)
		if u.shell.animating(time.Now()) || len(u.shell.liveDock.Order) != 0 {
			t.Fatal("short edit opened a dock after completion")
		}
		paint("native-dock-short-completed")
		p.ID, p.Complete = "stream", false
		u.shell.preview(p)
		time.Sleep(nativeDockReveal)
		u.shell.animating(time.Now())
		paint("native-dock-stream-revealed")
		p.Complete = true
		u.shell.preview(p)
		time.Sleep(nativeDockMinimum)
		u.shell.animating(time.Now())
		paint("native-dock-stream-settled")
	})
}

func TestNativeDockPendingLifecycle(t *testing.T) {
	for _, finish := range []string{"completed", "withdrawn", "turn ended", "reconnect"} {
		t.Run(finish, func(t *testing.T) {
			u := nativeBatchFixture(t)
			p := nativeBatchPreview("main", "/root", "main")
			p.Thread, p.Turn = "main", "turn"
			u.shell.preview(p)
			child := nativeBatchPreview("child", "/root/editor", "child")
			child.Thread, child.Turn = "child", "turn"
			u.shell.preview(child)
			switch finish {
			case "completed":
				u.shell.preview(diffview.Preview{ID: p.ID})
			case "withdrawn":
				u.shell.preview(diffview.Preview{ID: p.ID, Workspace: p.Workspace})
			case "turn ended":
				u.endTurnPreviews("main", "turn")
			case "reconnect":
				u.shell.applyNativeDiff(t.Context(), liveDiffEvent{Kind: "coverage", Status: "RECONNECTING: test"})
			}
			revealNativeDock(u.shell)
			if u.shell.liveDock.Views[p.ID] != nil || len(u.shell.livePending) != 0 {
				t.Fatal("retired pending preview reappeared")
			}
			if finish != "reconnect" && u.shell.liveDock.Views[child.ID] == nil {
				t.Fatal("another caller's pending preview was removed")
			}
		})
	}
}
