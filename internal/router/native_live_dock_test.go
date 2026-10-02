package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestNativeUIRunningChildRemovalReleasesDock(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	workspace := t.TempDir()
	broker := newLiveDiffBroker(t.Context())
	broker.scope = liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"child": true}}}
	u.shell.diff.scope = broker.scope
	sub := broker.subscribe()
	deliver := func() {
		for _, event := range broker.takePreviews(sub) {
			if event.Kind == "preview" {
				u.shell.applyNativeDiff(t.Context(), event)
				revealNativeDock(u.shell)
			}
		}
	}
	preview := diffview.Preview{ID: "running:child", Workspace: workspace, Thread: "child", Caller: "/root/worker", Status: diffview.PreviewRunning, Input: "observed edit"}
	broker.publishPreview(preview, false)
	deliver()
	if u.shell.liveDock.Live() != 1 {
		t.Fatal("running child never reached the native dock")
	}
	broker.discardRunningPreview(preview)
	deliver()
	if len(u.shell.liveDock.Order) != 0 || u.shell.animating(time.Now()) {
		t.Fatal("removed child retained a live card or animation loop")
	}
	screen := vt.NewEmulator(120, 40)
	defer screen.Close()
	if err := u.paint(screen, 120, 40); err != nil {
		t.Fatal(err)
	}
	if u.shell.layout.live.h != 0 || strings.Contains(screen.String(), "LIVE ·") {
		t.Fatalf("removed child still occupies dock space:\n%s", screen.String())
	}
}
