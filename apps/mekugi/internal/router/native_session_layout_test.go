package router

import (
	"strings"
	"testing"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestNativeUISessionLiveToggle(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.modelsLoading = true
	u.Settings.BeginLive()
	appServerTestKeys(t, u, "/live\r")
	defer u.shell.diffScreen.Close()
	if !u.shell.liveHidden || u.picker.open || u.Text != "" || wire.Len() != 0 {
		t.Fatal("toggle did not hide Live locally")
	}
	appServerTestKeys(t, u, "/live\r")
	if u.shell.liveHidden || u.picker.open || u.Text != "" || wire.Len() != 0 {
		t.Fatal("second toggle did not show Live locally")
	}
	for _, command := range []string{"/live off", "/live off", "/live on", "/live on"} {
		appServerTestKeys(t, u, command+"\r")
		if u.shell.liveHidden != strings.HasSuffix(command, "off") || u.picker.open || u.Text != "" || wire.Len() != 0 {
			t.Fatalf("direct setting failed: %s", command)
		}
	}
	for _, command := range []string{"/live invalid", "/live on extra"} {
		u.loadDraft(conversationmodel.Input{})
		appServerTestKeys(t, u, command+"\r")
		if u.shell.liveHidden || u.Text != command || !u.feedback.Alert {
			t.Fatal("invalid value changed preference or lost draft")
		}
	}
	if len(u.view.Entries) != 0 {
		t.Fatal("local toggle entered the transcript")
	}
	other, _ := newAppServerTestUI()
	other.ensureShell()
	defer other.shell.diffScreen.Close()
	if other.shell.liveHidden {
		t.Fatal("new session inherited preference")
	}
}

func TestNativeUILiveToggleDuringModelList(t *testing.T) {
	for _, reply := range []string{`{"id":1,"result":{"data":[]}}`, `{"id":1,"error":{"code":-1,"message":"unavailable"}}`} {
		u, wire := newAppServerTestUI()
		u.modelsLoading = true
		appServerTestRequest(t, u, "1", "model/list")
		appServerTestKeys(t, u, "/live\r")
		defer u.shell.diffScreen.Close()
		appServerTestMessage(t, u, reply)
		if !u.shell.liveHidden || u.picker.open || wire.Len() != 0 {
			t.Fatal("model response disturbed Live toggle")
		}
		appServerTestKeys(t, u, "/live\r")
		if u.shell.liveHidden || u.picker.open || wire.Len() != 0 {
			t.Fatal("interleaved reply prevented toggling back")
		}
	}
}

func TestUISnapshotNativeLiveToggle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.Painter.Theme = diffrender.DarkTheme
	u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.view.Clock = u.clock
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	screen := vt.NewEmulator(100, 28)
	defer screen.Close()
	preview := previewmodel.Preview{ID: "edit", Workspace: t.TempDir(), Caller: "/root", Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: live.txt\n+Live edit still in progress\n*** End Patch"}
	u.session.cwd = preview.Workspace
	u.shell.preview(projectStockPatchPreview(t.Context(), preview.Workspace, preview), false)
	revealNativeDock(u.shell)
	for _, value := range []string{"off", "on"} {
		appServerTestKeys(t, u, "/live\r")
		if err := u.paint(screen, 100, 28); err != nil {
			t.Fatal(err)
		}
		assertNativeUISnapshot(t, "native-live-toggle-"+value, strings.Split(screen.String(), "\n"))
	}
}

func TestNativeUISessionLayoutRendered(t *testing.T) {
	for _, caller := range []string{"/root", "/root/worker"} {
		for _, width := range []int{80, 120} {
			u, _ := newAppServerTestUI()
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			u.agents.Agents = []activityPaneAgent{{Name: "/root", Responding: true}, {Name: "/root/worker", Responding: true}}
			screen := vt.NewEmulator(width, 40)
			defer screen.Close()
			paint := func() string {
				t.Helper()
				if err := u.paint(screen, width, 40); err != nil {
					t.Fatal(err)
				}
				return screen.String()
			}
			workspace := t.TempDir()
			preview := previewmodel.Preview{ID: "edit", Workspace: workspace, Caller: caller, Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: a.txt\n+VISIBLE_EDIT\n*** End Patch"}
			u.shell.preview(projectStockPatchPreview(t.Context(), workspace, preview), false)
			revealNativeDock(u.shell)
			if caller != "/root" {
				u.agents.setDisplayFilter(true, caller)
			}
			if !strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("default dock missing")
			}
			u.agents.setDisplayFilter(false, u.agents.Selected)
			appServerTestKeys(t, u, "/live\r")
			u.agents.setDisplayFilter(caller != "/root", u.agents.Selected)
			frame := paint()
			if strings.Contains(frame, "VISIBLE_EDIT") || u.shell.layout.Live.H != 0 || len(u.shell.liveDock.Order) != 1 {
				t.Fatal("hidden dock lost state or reserved space")
			}
			lines := strings.Split(frame, "\n")
			r := u.shell.layout.Roster
			if r.H == 0 || !strings.Contains(lines[r.Y-1], "┘") || strings.TrimSpace(lines[r.Y+r.H]) != "" {
				t.Fatalf("roster must immediately follow the pane border with padding only below:\n%s", frame)
			}
			u.agents.setDisplayFilter(false, u.agents.Selected)
			appServerTestKeys(t, u, "/live\r")
			u.agents.setDisplayFilter(caller != "/root", u.agents.Selected)
			if !strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("current edit not restored")
			}
			appServerTestKeys(t, u, "/live\r")
			preview.Complete = true
			u.shell.preview(projectStockPatchPreview(t.Context(), workspace, preview), false)
			revealNativeDock(u.shell)
			u.shell.animating(time.Now().Add(time.Hour))
			appServerTestKeys(t, u, "/live\r")
			if strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("expired edit revived")
			}
		}
	}
}

func TestNativeUIComposerGap(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "draft"
	rows, _ := u.mainFrame(80, 24, 0)
	border := u.composerRect.Y - 1
	if len(rows) != 24 || strings.TrimSpace(ansi.Strip(rows[border-1])) != "" {
		t.Fatalf("composer not separated: %q", rows)
	}
}
