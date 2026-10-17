package router

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/activity"
	activityview "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestNativeAuxiliaryAbsencePreservesMainAndReviewSource(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.terminalViews = terminalsession.Views{}
	u.ensureShell()
	terminalTest(u).Shell.RestorePreferences(terminalsession.Preferences{Focus: 1, DiffOpen: true}, &u.mainView)
	u.review.Projection.Workspace, u.review.Projection.Coverage = "/workspace", "READY"
	u.publishReview()
	if err := u.testPaint(io.Discard, 160, 32); err != nil {
		t.Fatal(err)
	}
	shell := terminalTest(u)
	if shell.Auxiliary != nil || shell.Focus != 0 || shell.DiffOpen || shell.Layout.Diff.W != 0 || shell.Tabs[1].W != 0 {
		t.Fatalf("absent contribution retained interaction: %+v", shell.Shell)
	}
	if strings.Contains(shell.nativeStatus(), "Diff") {
		t.Fatal("absent pane retained a selector")
	}
	for _, key := range []byte("\x022hello") {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.Text != "hello" || wire.Len() != 0 {
		t.Fatalf("Main input changed: %q %q", u.Text, wire.String())
	}
	if view := u.reviewSource.ReviewView(u.session.Scope()); view.Workspace != "/workspace" || view.Coverage != "READY" {
		t.Fatal("removing a view removed its independent source publication")
	}
	if state := shell.Shell.Preferences(&shell.main.mainView); state.Focus != 0 || state.DiffOpen || state.NavigatorColumns != 0 {
		t.Fatalf("absent preference: %+v", state)
	}
}

func TestReplayAuxiliaryAbsenceSurvivesReset(t *testing.T) {
	owner, _ := newAppServerTestUI()
	start := time.Unix(1, 0)
	p := newUIReplayPlayback(terminalsession.Views{}, owner.presentationState, owner.conversationState, owner.observations, owner.activity, t.Context(), &playback.Source{Thread: "main", Start: start, End: start.Add(time.Second)}, 1)
	defer p.close()
	p.ui.shell.Focus, p.ui.shell.DiffOpen = 3, true
	p.reset()
	if p.ui.shell.Auxiliary != nil || p.ui.shell.RosterLabel != "" || p.ui.shell.Focus != 0 || p.ui.shell.DiffOpen {
		t.Fatal("replay recreated an absent contribution")
	}
	if err := p.paint(io.Discard, 120, 24); err != nil {
		t.Fatal(err)
	}
}

func TestUISnapshotNativeWithoutActivityRoster(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.terminalViews = terminalsession.Views{}
	u.activity = nil
	u.clock = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local) }
	u.ensureShell()
	defer terminalTest(u).Diff.Close()
	u.view.Clock, u.agents.Clock = u.clock, u.clock
	u.startSession("main", "/workspace")
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "reader"}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": thread + " Conversation result"}})
	}
	wire.Reset()
	terminalTest(u).Focus = 3
	screen := vt.NewEmulator(100, 24)
	defer screen.Close()
	paint := func(name, text string) {
		t.Helper()
		if err := u.testPaint(screen, 100, 24); err != nil {
			t.Fatal(err)
		}
		frame := screen.String()
		if !strings.Contains(frame, text) {
			t.Fatalf("Conversation missing: %s", frame)
		}
		if terminalTest(u).Layout.Roster.H != 0 || terminalTest(u).Tabs[3].W != 0 || len(u.agents.Hits) != 0 || strings.Contains(terminalTest(u).nativeStatus(), "Agents") {
			t.Fatal("absent roster retained rows or input targets")
		}
		assertNativeUISnapshot(t, name, strings.Split(frame, "\n"))
	}
	paint("native-no-roster-main", "main Conversation result")
	if terminalTest(u).Focus != 0 {
		t.Fatal("unavailable persisted focus was not clamped")
	}
	for _, key := range []byte("\x023retained draft") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.Text != "retained draft" || wire.Len() != 0 {
		t.Fatal("unavailable selector consumed ordinary input")
	}
	u.testActivity(u.agents).displayThread("/root/reader")
	paint("native-no-roster-child", "child Conversation result")
	if !u.composerReadOnly() || u.Thread != "main" || u.Text != "retained draft" {
		t.Fatal("child display changed Main input ownership")
	}
	terminalTest(u).selectNativePane(0)
	appServerTestKeys(t, u, "\r")
	appServerOneRequest(t, wire, "turn/start", "retained draft")
	if u.activity != nil {
		t.Fatal("renderer recreated the optional source service")
	}
}

func TestNativeRosterConstructorReplacementAndReplay(t *testing.T) {
	var c composition.Composition
	d := activity.Module
	var created []*activityview.View
	d.Views.New = func() *activityview.View {
		v := activityview.NewView()
		created = append(created, v)
		return v
	}
	c.Add(d, func() activity.Deps { return activity.Deps{} })
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	u, wire := newAppServerTestUI()
	u.terminalViews = runtime.Terminal
	u.ensureShell()
	defer terminalTest(u).Diff.Close()
	if len(created) != 1 || u.agents != created[0] {
		t.Fatal("native attachment bypassed descriptor")
	}
	appServerTestKeys(t, u, "/clear\r")
	req := appServerOneRequest(t, wire, "thread/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/workspace"}}}`, req.ID))
	if len(created) != 2 || u.agents != created[1] || terminalTest(u).Agents != created[1] {
		t.Fatal("replacement bypassed descriptor")
	}
	if err := u.testPaint(io.Discard, 100, 24); err != nil {
		t.Fatal(err)
	}
	if terminalTest(u).Layout.Roster.H == 0 || terminalTest(u).Tabs[3].W == 0 {
		t.Fatal("declared roster did not reach painting")
	}
	p := newUIReplayPlayback(runtime.Terminal, u.presentationState, u.conversationState, u.observations, u.activity, t.Context(), replayPlaybackTestSource(), 1)
	defer p.close()
	p.reset()
	if len(created) != 4 || p.ui.agents != created[3] {
		t.Fatal("replay reset bypassed descriptor")
	}
}
