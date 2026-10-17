package router

import (
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/vt"
)

func TestNativeUIRunningChildRemovalReleasesDock(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	workspace := t.TempDir()
	broker := newLivePreview(t.Context())
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"child": true}}}
	setLiveScope(broker, scope)
	u.shell.saved.scope = scope
	sub := testReview(broker).Subscribe()
	deliver := func() {
		for _, event := range testReview(broker).TakePreviews(sub) {
			if event.Kind == "preview" {
				u.shell.applyDiff(t.Context(), event)
				revealNativeDock(u.shell)
			}
		}
	}
	preview := previewmodel.Preview{ID: "running:child", Workspace: workspace, Thread: "child", Caller: "/root/worker", Status: previewmodel.PreviewRunning, Input: "observed edit"}
	broker.Update(preview, false)
	deliver()
	if u.shell.liveDock.Live() != 1 {
		t.Fatal("running child never reached the native dock")
	}
	broker.DiscardRunning(preview)
	deliver()
	if len(u.shell.liveDock.Order) != 0 || u.shell.animating(time.Now()) {
		t.Fatal("removed child retained a live card or animation loop")
	}
	screen := vt.NewEmulator(120, 40)
	defer screen.Close()
	if err := u.testPaint(screen, 120, 40); err != nil {
		t.Fatal(err)
	}
	if u.shell.Layout.Live.H != 0 || strings.Contains(screen.String(), "LIVE ·") {
		t.Fatalf("removed child still occupies dock space:\n%s", screen.String())
	}
}
