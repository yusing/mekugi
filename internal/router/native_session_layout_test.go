package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestNativeUISessionLivePicker(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.modelsLoading, u.settingsPending = true, true
	appServerTestKeys(t, u, "/live\r")
	if u.picker.loading || len(u.picker.choices) != 2 || u.picker.choices[u.picker.selected].name != "on" {
		t.Fatalf("Live picker unavailable: %+v", u.picker)
	}
	appServerTestKeys(t, u, "\x1b[B\r")
	if !u.shell.liveHidden || u.picker.open || wire.Len() != 0 {
		t.Fatal("local choice failed or reached host")
	}
	appServerTestKeys(t, u, "/live\r\x1b")
	if !u.shell.liveHidden {
		t.Fatal("cancel changed visibility")
	}
	appServerTestKeys(t, u, "/live on\r")
	if u.shell.liveHidden || u.draft != "" || wire.Len() != 0 {
		t.Fatal("direct setting failed")
	}
	appServerTestKeys(t, u, "/live invalid\r")
	if u.shell.liveHidden || u.draft != "/live invalid" || !u.noticeAlert {
		t.Fatal("invalid value changed preference or lost draft")
	}
	other, _ := newAppServerTestUI()
	other.ensureShell()
	defer other.shell.diffScreen.Close()
	if other.shell.liveHidden {
		t.Fatal("new session inherited preference")
	}
}

func TestNativeUILivePickerDuringModelList(t *testing.T) {
	for _, reply := range []string{`{"id":1,"result":{"data":[]}}`, `{"id":1,"error":{"code":-1,"message":"unavailable"}}`} {
		u, wire := newAppServerTestUI()
		u.ensureShell()
		defer u.shell.diffScreen.Close()
		u.modelsLoading = true
		u.requests["1"] = "model/list"
		appServerTestKeys(t, u, "/live\r\x1b[B")
		appServerTestMessage(t, u, reply)
		if u.picker.loading || u.picker.problem != "" || u.picker.choices[u.picker.selected].name != "off" {
			t.Fatalf("model response disturbed Live choice: %+v", u.picker)
		}
		appServerTestKeys(t, u, "\r")
		if !u.shell.liveHidden || wire.Len() != 0 {
			t.Fatal("interleaved reply changed submitted choice")
		}
	}
}

func TestNativeUISessionLayoutRendered(t *testing.T) {
	for _, caller := range []string{"/root", "/root/worker"} {
		for _, width := range []int{80, 120} {
			u, _ := newAppServerTestUI()
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			u.agents.agents = []activityPaneAgent{{Name: "/root", Responding: true}, {Name: "/root/worker", Responding: true}}
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
			preview := diffview.Preview{ID: "edit", Workspace: workspace, Caller: caller, Status: diffview.PreviewEdit, Input: "*** Begin Patch\n*** Add File: a.txt\n+VISIBLE_EDIT\n*** End Patch"}
			u.shell.preview(projectStockPatchPreview(t.Context(), workspace, preview))
			if !strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("default dock missing")
			}
			u.setLivePane("off")
			frame := paint()
			if strings.Contains(frame, "VISIBLE_EDIT") || u.shell.layout.live.h != 0 || len(u.shell.liveDock.Order) != 1 {
				t.Fatal("hidden dock lost state or reserved space")
			}
			lines := strings.Split(frame, "\n")
			r := u.shell.layout.roster
			if r.h == 0 || !strings.Contains(lines[r.y-1], "┘") || strings.TrimSpace(lines[r.y+r.h]) != "" {
				t.Fatalf("roster must immediately follow the pane border with padding only below:\n%s", frame)
			}
			u.setLivePane("on")
			if !strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("current edit not restored")
			}
			u.setLivePane("off")
			preview.Complete = true
			u.shell.preview(projectStockPatchPreview(t.Context(), workspace, preview))
			u.shell.animating(time.Now().Add(time.Hour))
			u.setLivePane("on")
			if strings.Contains(paint(), "VISIBLE_EDIT") {
				t.Fatal("expired edit revived")
			}
		}
	}
}

func TestNativeUIComposerGapBelowJournal(t *testing.T) {
	for _, withJournal := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		u.draft = "draft"
		if withJournal {
			tree := nativeJournalFixture()
			u.journal = &nativeJournalSink{tree: &tree}
		}
		rows, _ := u.mainFrame(80, 24, 0)
		border := u.composerRect.y - 1
		if len(rows) != 24 || strings.TrimSpace(ansi.Strip(rows[border-1])) != "" {
			t.Fatalf("composer not separated: %q", rows)
		}
		if withJournal && !strings.Contains(ansi.Strip(rows[border-2]), "Working renderer") {
			t.Fatalf("journal not above gap: %q", rows)
		}
	}
}
