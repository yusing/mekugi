package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestNativeUISessionLiveToggle(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.modelsLoading = true
	u.Settings.Admit(sessioncontrol.SettingsPlan{Action: sessioncontrol.SettingsTurn})
	appServerTestKeys(t, u, "/live\r")
	defer terminalTest(u).Diff.Close()
	if !terminalTest(u).LiveHidden || u.picker.Open || u.Text != "" || wire.Len() != 0 {
		t.Fatal("toggle did not hide Live locally")
	}
	appServerTestKeys(t, u, "/live\r")
	if terminalTest(u).LiveHidden || u.picker.Open || u.Text != "" || wire.Len() != 0 {
		t.Fatal("second toggle did not show Live locally")
	}
	for _, command := range []string{"/live off", "/live off", "/live on", "/live on"} {
		appServerTestKeys(t, u, command+"\r")
		if terminalTest(u).LiveHidden != strings.HasSuffix(command, "off") || u.picker.Open || u.Text != "" || wire.Len() != 0 {
			t.Fatalf("direct setting failed: %s", command)
		}
	}
	for _, command := range []string{"/live invalid", "/live on extra"} {
		u.loadDraft(conversationmodel.Input{})
		appServerTestKeys(t, u, command+"\r")
		if terminalTest(u).LiveHidden || u.Text != command || !u.feedback.Alert {
			t.Fatal("invalid value changed preference or lost draft")
		}
	}
	if len(u.source.Entries) != 0 {
		t.Fatal("local toggle entered the transcript")
	}
	other, _ := newAppServerTestUI()

	defer terminalTest(other).Diff.Close()
	if terminalTest(other).LiveHidden {
		t.Fatal("new session inherited preference")
	}
}

func TestNativeUILiveToggleDuringModelList(t *testing.T) {
	for _, reply := range []string{`{"id":1,"result":{"data":[]}}`, `{"id":1,"error":{"code":-1,"message":"unavailable"}}`} {
		u, wire := newAppServerTestUI()
		u.modelsLoading = true
		appServerTestRequest(t, u, "1", "model/list")
		appServerTestKeys(t, u, "/live\r")
		defer terminalTest(u).Diff.Close()
		appServerTestMessage(t, u, reply)
		if !terminalTest(u).LiveHidden || u.picker.Open || wire.Len() != 0 {
			t.Fatal("model response disturbed Live toggle")
		}
		appServerTestKeys(t, u, "/live\r")
		if terminalTest(u).LiveHidden || u.picker.Open || wire.Len() != 0 {
			t.Fatal("interleaved reply prevented toggling back")
		}
	}
}

func TestUISnapshotNativeLiveToggle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.sessionPresentation().Status.Text, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.shell.Transcript.Clock = u.clock

	defer terminalTest(u).Diff.Close()
	screen := vt.NewEmulator(100, 28)
	defer screen.Close()
	preview := previewmodel.Preview{ID: "edit", Workspace: t.TempDir(), Caller: "/root", Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: live.txt\n+Live edit still in progress\n*** End Patch"}
	u.cwd = preview.Workspace
	u.preview(projectStockPatchPreview(t.Context(), preview.Workspace, preview), false)
	revealNativeDock(terminalTest(u))
	for _, value := range []string{"off", "on"} {
		appServerTestKeys(t, u, "/live\r")
		if err := u.testPaint(screen, 100, 28); err != nil {
			t.Fatal(err)
		}
		assertNativeUISnapshot(t, "native-live-toggle-"+value, strings.Split(screen.String(), "\n"))
	}
}

func TestNativeUISessionLayoutRendered(t *testing.T) {
	for _, caller := range []string{"/root", "/root/worker"} {
		for _, width := range []int{80, 120} {
			u, _ := newAppServerTestUI()

			defer terminalTest(u).Diff.Close()
			u.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root", Responding: true}, {Name: "/root/worker", Responding: true}}
			screen := vt.NewEmulator(width, 40)
			defer screen.Close()
			paint := func() string {
				t.Helper()
				if err := u.testPaint(screen, width, 40); err != nil {
					t.Fatal(err)
				}
				return screen.String()
			}
			workspace := t.TempDir()
			preview := previewmodel.Preview{ID: "edit", Workspace: workspace, Caller: caller, Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Add File: a.txt\n+VISIBLE_EDIT\n*** End Patch"}
			u.preview(projectStockPatchPreview(t.Context(), workspace, preview), false)
			revealNativeDock(terminalTest(u))
			if caller != "/root" {
				u.testActivity(u.shell.Agents).setDisplayFilter(true, caller)
			}
			if !strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("default dock missing")
			}
			u.testActivity(u.shell.Agents).setDisplayFilter(false, u.shell.Agents.Selected)
			appServerTestKeys(t, u, "/live\r")
			u.testActivity(u.shell.Agents).setDisplayFilter(caller != "/root", u.shell.Agents.Selected)
			frame := paint()
			if strings.Contains(frame, "VISIBLE_EDIT") || terminalTest(u).Layout.Live.H != 0 || len(terminalTest(u).Live.Order) != 1 {
				t.Fatal("hidden dock lost state or reserved space")
			}
			lines := strings.Split(frame, "\n")
			r := terminalTest(u).Layout.Roster
			if r.H == 0 || !strings.Contains(lines[r.Y-1], "┘") || strings.TrimSpace(lines[r.Y+r.H]) != "" {
				t.Fatalf("roster must immediately follow the pane border with padding only below:\n%s", frame)
			}
			u.testActivity(u.shell.Agents).setDisplayFilter(false, u.shell.Agents.Selected)
			appServerTestKeys(t, u, "/live\r")
			u.testActivity(u.shell.Agents).setDisplayFilter(caller != "/root", u.shell.Agents.Selected)
			if !strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("current edit not restored")
			}
			appServerTestKeys(t, u, "/live\r")
			preview.Complete = true
			u.preview(projectStockPatchPreview(t.Context(), workspace, preview), false)
			revealNativeDock(terminalTest(u))
			terminalTest(u).Live.Tick(time.Now().Add(time.Hour))
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
	rows, _ := u.testMainFrame(80, 24, 0)
	border := u.shell.Main.ComposerRect.Y - 1
	if len(rows) != 24 || strings.TrimSpace(ansi.Strip(rows[border-1])) != "" {
		t.Fatalf("composer not separated: %q", rows)
	}
}
