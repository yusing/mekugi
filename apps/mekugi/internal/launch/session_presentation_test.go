package launch

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func TestNonTerminalDisplayPublishesWithoutActivityOrPaint(t *testing.T) {
	for _, activity := range []bool{true, false} {
		t.Run(map[bool]string{true: "composed", false: "without Activity"}[activity], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if !activity {
				u.activity = nil
			}
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			editOrdinaryInput(t, u, "Main draft")
			before := u.attached.presentation.View()
			if !displaySession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, sessionpresentation.DisplayIntent{Version: before.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}}) {
				t.Fatal(u.attached.feedback.Text)
			}
			view := u.attached.presentation.View()
			if view.ReadOnly != sessionpresentation.ChildDisplayed || view.Status.Composer.Text != "Read-only" || view.Input != before.Input || view.Draft.Draft.Text != "Main draft" {
				t.Fatalf("display did not publish a complete read-only view: %+v", view)
			}
			if displaySession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, sessionpresentation.DisplayIntent{Version: before.Version, Thread: before.Main}) || u.attached.presentation.View().Displayed.ID != "child" {
				t.Fatal("stale display changed the selected thread")
			}
			view = u.attached.presentation.View()
			if !displaySession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main}) || u.attached.presentation.View().ReadOnly != sessionpresentation.Editable || u.Text != "Main draft" {
				t.Fatal("Main did not regain its retained draft")
			}
		})
	}
}

func TestSessionPaintDoesNotRefreshPublication(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Unix(100, 0)
	u.clock = func() time.Time { return now }
	appServerTestTurn(t, u, "turn")
	questionTestSync(t, u, "request", false)
	before := u.attached.presentation.View()
	if before.Questions.Call == nil || u.shell.Prompts.Ready() {
		t.Fatal("prompt publication did not precede its display receipt")
	}
	now = now.Add(time.Second)
	for range 2 {
		if err := paintSession(&u.attached, u.shell, &u.detailFiles, u.ctx, presentationTime(u.clock), io.Discard, 100, 24); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(u.attached.presentation.View(), before) {
			t.Fatal("paint refreshed the publication or changed its version")
		}
	}
	if !u.shell.Prompts.Ready() {
		t.Fatal("successful visible delivery did not acknowledge the prompt")
	}
}

// Tests that arrange a feed filter also retain the renderer's remembered row.
// Production selection enters through roster keys, clicks and Main selection.
func (v *activityAdapter) setDisplayFilter(child bool, caller string) {
	v.Selected = caller
	if child {
		v.displayThread(caller)
	} else {
		v.displayThread("/root")
	}
}

func TestNativeDisplayedThreadIndependentOfRosterFocus(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	for _, thread := range []string{"first", "second"} {
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": thread}})
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "work"}})
	}
	first := u.attached.session.Transcript("first", "work").Turn
	u.testActivity(u.shell.Agents).displayThread("/root/first")
	u.shell.Agents.Selected = "/root/second"
	u.testActivity(u.shell.Agents).testKeepSelection()
	view := u.attached.presentation.View()
	if view.Displayed.ID != "first" || view.Input.ID != "main" || !(u.attached.presentation.View().ReadOnly != sessionpresentation.Editable) || view.Displayed.Caller != "/root/first" {
		t.Fatalf("roster focus changed display or admission: %+v", view)
	}
	if !u.testActivity(u.shell.Agents).testVisible(sessionpresentation.ActivityEntry{Agent: "/root/first", Kind: "text"}) || u.testActivity(u.shell.Agents).testVisible(sessionpresentation.ActivityEntry{Agent: "/root/second", Kind: "text"}) {
		t.Fatal("transcript followed the focused roster row")
	}
	terminalTest(u).selectNativePane(0)
	if (u.attached.presentation.View().ReadOnly != sessionpresentation.Editable) || transcriptView(u.attached.presentation, u.shell) != u.shell.Transcript || u.shell.Agents.Selected != "/root/second" {
		t.Fatal("Main selection changed roster focus or retained child admission")
	}
	if first == nil || u.attached.session.Transcript("first", "work").Turn != first || !u.attached.session.Agent("/root/first").Responding {
		t.Fatal("display switching retired a child's work scope")
	}
}

func TestNativeDisplaySwitchKeepsHistoryObservation(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "saved", 109000)}, "nextCursor": "older"})
	u.testActivity(u.shell.Agents).displayThread("/root/worker")
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	id := historyLoading(&u.attached, &u.childHistoryState).state.RequestID()
	terminalTest(u).selectNativePane(0)
	if historyLoading(&u.attached, &u.childHistoryState) == nil || historyLoading(&u.attached, &u.childHistoryState).state.RequestID() != id || len(restoreContentRequests(t, wire)) != 6 {
		t.Fatal("display switching retired the observation or dispatched host work")
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "older", 101000)}})
	if historyLoading(&u.attached, &u.childHistoryState) != nil || u.attached.presentation.View().ReadOnly != sessionpresentation.Editable {
		t.Fatal("background history settlement changed Main admission")
	}
}

func (v *activityAdapter) displayThread(caller string) {
	if caller != "" && caller != "/root" {
		v.Selected = caller
	}
	_ = v.display.Display(sessionpresentation.DisplayIntent{Version: v.display.View().Version, Thread: v.displayTarget(caller)})
	v.publishActivity()
}
func (v *activityAdapter) applyDisplayIntent() {
	if intent := v.takeDisplayIntent(); intent != nil {
		if err := v.display.Display(*intent); err == nil && intent.Thread.Caller != "/root" {
			v.Selected = intent.Thread.Caller
		}
		v.publishActivity()
	}
}

func TestPromptFailedFrameDoesNotAcknowledgeOpening(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestTurn(t, u, "turn")
	questionTestSync(t, u, "request", false)
	failure := errors.New("terminal write failed")
	if err := paintSession(&u.attached, u.shell, &u.detailFiles, u.ctx, presentationTime(u.clock), serverErrorWriter{err: failure}, 100, 24); !errors.Is(err, failure) {
		t.Fatalf("write failure: %v", err)
	}
	if u.shell.Prompts.Ready() {
		t.Fatal("failed frame readied prompt")
	}
	if err := paintSession(&u.attached, u.shell, &u.detailFiles, u.ctx, presentationTime(u.clock), io.Discard, 100, 24); err != nil {
		t.Fatal(err)
	}
	if !u.shell.Prompts.Ready() {
		t.Fatal("successful retry did not ready prompt")
	}
}

func TestMainFrameConsumesPublishedMetadata(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.title, u.backendVersion = "Published title", "1.2.3"
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	before := u.attached.presentation.View()
	u.title, u.backendVersion, u.attached.cwd = "Unpublished title", "9.9.9", "/unpublished"
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Unpublished activity"}}})
	rows, _ := u.mainFrame(100, 24, 0)
	painted := strings.Join(rows, "\n")
	if !strings.Contains(painted, "codex v1.2.3") || strings.Contains(painted, "9.9.9") || strings.Contains(painted, "Unpublished activity") || !strings.Contains(u.mainHeaderRight(100, true), "Published title") {
		t.Fatalf("Main consulted unpublished owners: %s", painted)
	}
	if err := paintSession(&u.attached, u.shell, &u.detailFiles, u.ctx, presentationTime(u.clock), io.Discard, 100, 24); err != nil {
		t.Fatal(err)
	}
	outer := strings.Join(terminalTest(u).Frame.Rows, "\n")
	if !strings.Contains(outer, "Published title") || !strings.Contains(outer, "codex v1.2.3") || strings.Contains(outer, "Unpublished") {
		t.Fatalf("outer frame consulted unpublished owners: %s", outer)
	}
	if !reflect.DeepEqual(u.attached.presentation.View(), before) {
		t.Fatal("painting refreshed the session publication")
	}
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	rows, _ = u.mainFrame(100, 24, 0)
	if !strings.Contains(strings.Join(rows, "\n"), "Unpublished activity") || !strings.Contains(u.mainHeaderRight(100, true), "Unpublished title") || u.attached.presentation.View().Info.Workspace != "/unpublished" {
		t.Fatal("explicit publication did not advance Main")
	}
}
