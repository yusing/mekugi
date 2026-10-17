package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestUISnapshotNativeThreadSwitch(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "reader"}})
	u.Text = "retained draft"
	for _, settings := range []struct{ thread, model, effort, tier string }{
		{"main", "parent-model", "high", "priority"},
		{"child", "child-model", "low", "flex"},
	} {
		appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": settings.thread, "threadSettings": map[string]any{"model": settings.model, "effort": settings.effort, "serviceTier": settings.tier}})
	}
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "child", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 25000}, "modelContextWindow": 100000}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": thread + " result\n\n" + strings.Join([]string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta"}, "\n\n")}})
	}
	screen := vt.NewEmulator(140, 36)
	defer screen.Close()
	paint := func(name string) string {
		t.Helper()
		if err := u.testPaint(screen, 140, 36); err != nil {
			t.Fatal(err)
		}
		frame := screen.String()
		assertNativeUISnapshot(t, name, strings.Split(frame, "\n"))
		return frame
	}
	keys := func(input string) {
		t.Helper()
		for _, key := range []byte(input) {
			if err := terminalTest(u).key(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	paint("native-thread-main-full")
	if terminalTest(u).Layout.Main.W != 138 || terminalTest(u).Layout.Vertical != -1 {
		t.Fatalf("initial geometry: %+v", terminalTest(u).Layout)
	}
	click := func(agent string) {
		t.Helper()
		for _, hit := range u.shell.Agents.Hits {
			if hit.Agent == agent && !hit.Skills {
				r := terminalTest(u).Layout.Roster
				for _, suffix := range []string{"M", "m"} {
					if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", r.X+hit.First, r.Y+hit.Row, suffix)); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
		}
		t.Fatalf("missing roster target %q", agent)
	}
	click("/root/reader")
	frame := paint("native-thread-child-full")
	if !strings.Contains(frame, "child result") || !strings.Contains(frame, "eta") || strings.Contains(frame, "main result") || !strings.Contains(frame, "Read-only") || u.Thread != "main" {
		t.Fatalf("child view or input identity: %s", frame)
	}
	if !strings.Contains(frame, "child-model (low) · flex") || !strings.Contains(frame, "25K/100K • 25%") || strings.Contains(frame, "parent-model") || strings.Contains(frame, u.Text) {
		t.Fatalf("child composer: %s", frame)
	}
	keys("\x022")
	paint("native-thread-child-split")
	if terminalTest(u).Layout.Diff.W == 0 || !u.testActivity(u.shell.Agents).displayingChild() {
		t.Fatal("opening Diff lost the displayed child")
	}
	keys("\x022")
	paint("native-thread-child-unsplit")
	if terminalTest(u).Layout.Main.W != 138 || terminalTest(u).Layout.Diff.W != 0 || terminalTest(u).Focus != 0 {
		t.Fatal("closing Diff did not reclaim its width")
	}
	// Metadata discovery is independent of the composer destination.
	w := u.Host.Input.(*appServerTestInput)
	appServerOneRequest(t, w, "thread/read", "")
	u.Turn = "active"
	keys("/skills\r\t\x03\x07\x16\x7f\x1b[1;2A\x1b[200~pasted\x03\r\x1b[201~")
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.Text != "retained draft" || u.picker.Open || terminalTest(u).Decoder.Pasting() || u.quitRequested || u.Turn != "active" || w.Len() != 0 {
		t.Fatal("read-only composer accepted editing, paste, submission or interruption")
	}
	u.Turn = ""
	if err := u.testPaint(screen, 140, 36); err != nil {
		t.Fatal(err)
	}
	click("/root")
	frame = paint("native-thread-return-main")
	if u.testActivity(u.shell.Agents).displayingChild() || !strings.Contains(frame, "main result") || strings.Contains(frame, "Input: Main") || u.Text != "retained draft" {
		t.Fatal("returning to Main lost its transcript or draft")
	}
	keys("\x021\r")
	request := appServerOneRequest(t, w, "turn/start", "retained draft")
	if request.Params.ThreadID != "main" {
		t.Fatal("restored composer targeted another thread")
	}
}

func TestNativeChildReferenceReturnsToMain(t *testing.T) {
	u := selectionTestUI("child answer")
	u.Agents = activityui.NewView()
	u.main.shell.Agents = u.Agents
	u.main.Text = "draft "
	u.main.testActivity(u.main.shell.Agents).setDisplayFilter(true, "/root/reader")
	publishPresentation(&u.main.attached, u.main.shell, &u.main.Client, &u.main.picker, &u.main.Editor, &u.main.childHistoryState, preferences.Applied{Model: u.main.model, Effort: u.main.reasoningEffort, Tier: u.main.serviceTier}, u.main.serviceTiers, u.main.title, u.main.backendVersion, u.main.restoring != nil, u.main.side, presentationTime(u.main.clock))
	u.main.shell.Agents.FeedLeft, u.main.shell.Agents.FeedTop = 1, 1
	u.main.shell.Agents.FeedRight, u.main.shell.Agents.FeedRows = 40, 1
	selectionTestDrag(t, u, 0, 0, 4, 0)
	u.selectionAction('r')
	if (u.main.attached.presentation.View().ReadOnly != sessionpresentation.Editable) || !strings.HasPrefix(u.main.Text, "draft ") || len(u.main.Selections) != 1 || u.Focus != 0 {
		t.Fatal("child reference did not return to the preserved Main draft")
	}
}

func TestNativeChildComposerSettingsEvidence(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.model, u.reasoningEffort, u.serviceTier = "parent-model", "high", "priority"

	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/reader")
	caption := func() string {
		rows, _ := u.testMainFrame(120, 20, 0)
		return ansi.Strip(rows[len(rows)-1])
	}
	if got := caption(); strings.Contains(got, "parent-model") || strings.Contains(got, "priority") {
		t.Fatalf("unknown child borrowed parent settings: %s", got)
	}
	info := resumeSettingsRollout(t, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"saved-model","reasoning_effort":"medium","service_tier":"flex"}}}`, "")
	info.AgentNickname = "reader"
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": info})
	if got := caption(); !strings.Contains(got, "saved-model (medium) · flex") {
		t.Fatalf("child metadata did not restore settings: %s", got)
	}
	appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": info.ID, "threadSettings": map[string]any{"model": "live-model", "effort": "low", "serviceTier": nil}})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": info})
	if got := caption(); !strings.Contains(got, "live-model (low) · default") || strings.Contains(got, "saved-model") {
		t.Fatalf("late metadata replaced live child settings: %s", got)
	}
	u.serviceTiers = inferencegateway.NewTiers(map[string]string{"live-model": "priority"})
	if got := caption(); !strings.Contains(got, "live-model (low) · priority") {
		t.Fatalf("child tier ignored model routing: %s", got)
	}
	u.serviceTiers.Confirm("live-model", info.ID, "flex")
	publication := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if publication.Caption.Model != "live-model" || publication.Caption.Tier != "flex" || publication.Status.Composer.Text != "Read-only" {
		t.Fatalf("detached child caption lost confirmed thread settings: %+v", publication)
	}
	if got := caption(); !strings.Contains(got, "live-model (low) · flex") {
		t.Fatalf("child caption ignored its confirmed thread tier: %s", got)
	}
	u.attached.session.Collaboration((binding.ThreadItem{Tool: "spawnAgent", SenderThreadID: "main", ReceiverThreadIDs: []string{"spawned"}, Model: "spawn-model", ReasoningEffort: "high"}).Observation(), "spawn", presentationTime(u.clock), activitypresentation.ObservedCollaboration)
	u.testActivity(u.shell.Agents).displayThread(u.attached.session.KnownCaller("spawned"))
	if got := caption(); !strings.Contains(got, "spawn-model (high)") || strings.Contains(got, "priority") {
		t.Fatalf("spawn metadata was not scoped to the selected child: %s", got)
	}
	if publication.Caption.Model != "live-model" || publication.Caption.Tier != "flex" {
		t.Fatal("switching threads mutated the retained caption")
	}
}

func TestUISnapshotNativeTinyDiffToggle(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")

	screen := vt.NewEmulator(80, 6)
	defer screen.Close()
	for _, key := range []byte("\x022") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.testPaint(screen, 80, 6); err != nil {
		t.Fatal(err)
	}
	assertNativeUISnapshot(t, "native-thread-tiny-diff", strings.Split(screen.String(), "\n"))
	if terminalTest(u).Layout.Diff.H == 0 || !strings.Contains(screen.String(), "CONNECTING") {
		t.Fatal("opening Diff left a short terminal blank")
	}
	for _, key := range []byte("\x022") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.testPaint(screen, 80, 6); err != nil {
		t.Fatal(err)
	}
	if terminalTest(u).Layout.Main.W != 80 || terminalTest(u).Layout.Diff.H != 0 {
		t.Fatal("closing Diff did not restore the short transcript")
	}
}
