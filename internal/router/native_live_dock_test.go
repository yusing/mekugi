package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestNativeUISharedLiveDockMainPriority(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.agents.agents = []activityPaneAgent{{Name: "/root/worker", Responding: true}}
	u.agents.only, u.agents.selected = true, "/root/worker"
	workspace := t.TempDir()
	add := func(id, caller, line string) {
		u.shell.preview(projectStockPatchPreview(t.Context(), workspace, diffview.Preview{
			ID: id, Workspace: workspace, Caller: caller, Status: diffview.PreviewEdit,
			Input: "*** Begin Patch\n*** Add File: " + id + ".txt\n+" + line + "\n*** End Patch",
		}))
	}
	add("child", "/root/worker", "CHILD_BODY")
	screen := vt.NewEmulator(160, 48)
	defer screen.Close()
	paint := func() string {
		t.Helper()
		if err := u.paint(screen, 160, 48); err != nil {
			t.Fatal(err)
		}
		return screen.String()
	}
	paint() // The child already owns the expanded card before Main arrives.
	add("main", "/root", "MAIN_BODY")
	frame := paint()
	if strings.Count(frame, "LIVE ·") != 1 || !strings.Contains(frame, "MAIN_BODY") || strings.Contains(frame, "CHILD_BODY") {
		t.Fatalf("Main did not win the single shared accordion:\n%s", frame)
	}
	u.shell.nextLive()
	if frame = paint(); !strings.Contains(frame, "CHILD_BODY") || strings.Contains(frame, "MAIN_BODY") {
		t.Fatalf("explicit cycling did not open the child:\n%s", frame)
	}
	// Completion markers carry only an ID. They must reach a child's card too.
	u.shell.preview(diffview.Preview{ID: "child"})
	if !u.shell.liveDock.Views["child"].Complete {
		t.Fatal("caller-less child completion was lost")
	}
	if frame = paint(); !strings.Contains(frame, "MAIN_BODY") {
		t.Fatalf("completed child retained priority over Main:\n%s", frame)
	}
}

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
