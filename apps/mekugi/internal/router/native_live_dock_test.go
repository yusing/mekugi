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

	defer terminalTest(u).Diff.Close()
	workspace := t.TempDir()
	broker := newLivePreview(t.Context())
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"child": true}}}
	setLiveScope(broker, scope)
	u.review.Projection.Scope = scope
	sub := testReview(broker).Subscribe()
	deliver := func() {
		for _, event := range testReview(broker).TakePreviews(sub) {
			if event.Kind == "preview" {
				u.applyReview(t.Context(), event)
				revealNativeDock(terminalTest(u))
			}
		}
	}
	preview := previewmodel.Preview{ID: "running:child", Workspace: workspace, Thread: "child", Caller: "/root/worker", Status: previewmodel.PreviewRunning, Input: "observed edit"}
	broker.Update(preview, false)
	deliver()
	if terminalTest(u).Live.Live() != 1 {
		t.Fatal("running child never reached the native dock")
	}
	broker.DiscardRunning(preview)
	deliver()
	if len(terminalTest(u).Live.Order) != 0 || terminalTest(u).Live.Tick(time.Now()) {
		t.Fatal("removed child retained a live card or animation loop")
	}
	screen := vt.NewEmulator(120, 40)
	defer screen.Close()
	if err := u.testPaint(screen, 120, 40); err != nil {
		t.Fatal(err)
	}
	if terminalTest(u).Layout.Live.H != 0 || strings.Contains(screen.String(), "LIVE ·") {
		t.Fatalf("removed child still occupies dock space:\n%s", screen.String())
	}
}
