package launch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestNativeViewsBorrowExecutionAndDisposeIndependently(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	u.Text = "private local draft"
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	firstContext, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	for id, ctx := range map[string]context.Context{"first": firstContext, "second": t.Context()} {
		request := sessionapi.NativeRequest{Attach: true, Attachment: id, Thread: "main", Workspace: "/workspace", Context: ctx, ViewContext: ctx}
		if refusal := n.attach(request, &u.attached, u.Thread); refusal != "" {
			t.Fatal(refusal)
		}
		view := n.views[id]
		view.attached.presentation.BorrowExecutionStatus(u.attached.presentation)
		publishSessionView(&view.attached, &u.Client, &view.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, true)
	}
	first, second := n.views["first"], n.views["second"]
	if first.attached.presentation.View().Draft.Draft.Text != "" || second.attached.presentation.View().Draft.Draft.Text != "" || u.Text != "private local draft" {
		t.Fatal("native view retained a copy of a client editor")
	}
	executionScope := u.attached.session.Scope()
	if first.attached.executionPresentation != u.attached.executionPresentation || second.attached.executionPresentation != u.attached.executionPresentation || first.scope.Scope() == executionScope || first.scope.Scope() == second.scope.Scope() {
		t.Fatal("views copied execution or shared presentation scope")
	}
	if first.picker.State == second.picker.State || first.picker.State == &u.attached.conversation.Completion {
		t.Fatal("view completion state shares another client's state")
	}
	intent := sessionpresentation.DisplayIntent{Version: first.attached.presentation.View().Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}}
	if refusal := first.applyView(sessionapi.NativeOperation{Display: &intent}); refusal != "" {
		t.Fatal(refusal)
	}
	if first.attached.presentation.View().Displayed.ID != "child" || second.attached.presentation.View().Displayed.ID != "main" || u.attached.presentation.View().Displayed.ID != "main" || wire.Len() != 0 {
		t.Fatal("view navigation changed another view or execution")
	}
	disconnect()
	select {
	case id := <-n.closed:
		if id != "first" {
			t.Fatalf("wrong view retired: %q", id)
		}
		n.retire(id)
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not release its presentation scope")
	}
	if n.views["first"] != nil || n.views["second"] != second || first.scope.Scope() != nil || u.attached.session.Scope() != executionScope || u.attached.conversation == nil {
		t.Fatal("view disposal retained its scope or retired execution/sibling state")
	}
	if _, refusal := n.admit(sessionapi.NativeRequest{Attachment: "first", Context: t.Context()}, &u.attached, u.Thread); refusal == "" {
		t.Fatal("late operation revived a disconnected view")
	}
}

func TestNativeViewOutputDemandUsesBorrowedSource(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	request := sessionapi.NativeRequest{Attach: true, Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
	if refusal := n.attach(request, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	output := u.activity.Outputs(u.attached.session.Scope()).New()
	output.Finish(new("retained server output\n"), new(0))
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 7, Agent: "Main", Kind: "tool", Native: &sessionpresentation.NativeItem{Output: output}}}})
	demand := activity.OutputDemand{Source: 7, Output: 1, Version: output.Version()}
	view := n.views["view"]
	if refusal := view.applyView(sessionapi.NativeOperation{Output: &sessionapi.NativeOutputDemand{Demands: []activity.OutputDemand{demand}}}); refusal != "" {
		t.Fatal(refusal)
	}
	if len(view.mainOutput) != 1 || view.mainOutput[0].Demand != demand || !strings.Contains(strings.Join(view.mainOutput[0].View.Lines, "\n"), "retained server output") {
		t.Fatalf("output demand lost identity or content: %+v", view.mainOutput)
	}
	n.retire("view")
	if output.View().Released {
		t.Fatal("closing a view released execution-owned output")
	}
}

func TestNativeStartupViewHasNoFabricatedThread(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Thread, u.attached.cwd = "", "/workspace"
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	request := sessionapi.NativeRequest{Attach: true, Attachment: "startup", Startup: "operation", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
	if refusal := n.attach(request, &u.attached, ""); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["startup"]
	view := publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, true)
	if view.Main.ID != "" || view.Info.Workspace != "/workspace" || view.Version.Attachment == 0 {
		t.Fatalf("startup publication fabricated a confirmed thread: %+v", view)
	}
}

func nativeSideSettingsTest(t *testing.T) (*appServerUI, *appServerTestInput, *nativeSessionView) {
	t.Helper()
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.model, u.reasoningEffort = "test", "low"
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, wire, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"},"model":"test","reasoningEffort":"low"}`)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	t.Cleanup(n.close)
	request := sessionapi.NativeRequest{Attach: true, Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
	if refusal := n.attach(request, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	view := n.views["view"]
	publishSessionView(&view.attached, &u.Client, &view.picker, "", preferences.Applied{Model: u.model, Effort: u.reasoningEffort}, u.serviceTiers, "", "", false, u.side, true)
	display := sessionpresentation.DisplayIntent{Version: view.attached.presentation.View().Version, Thread: view.attached.presentation.View().SideThread}
	if refusal := view.applyView(sessionapi.NativeOperation{Display: &display}); refusal != "" {
		t.Fatal(refusal)
	}
	if !view.attached.presentation.DisplayingSide() || u.attached.presentation.DisplayingSide() {
		t.Fatal("fixture did not keep native Side independent of execution Main")
	}
	return u, wire, view
}

func TestNativeSideReasoningWaitsForCatalog(t *testing.T) {
	u, wire, view := nativeSideSettingsTest(t)
	u.modelsLoading = true
	appServerTestRequest(t, u, "99", "model/list")
	if err := stepReasoning(&view.attached, view.shell, &u.Client, preferences.Applied{Model: u.model, Effort: u.reasoningEffort}, &u.serviceTiers, u.panes, false, u.clock, u.models, u.modelsLoading, true); err != nil {
		t.Fatal(err)
	}
	appServerTestMessage(t, u, `{"id":99,"result":{"data":[{"model":"test","defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}]}}`)
	if wire.Len() == 0 {
		t.Fatalf("unchanged native Side shortcut discarded; execution feedback=%q native feedback=%q", u.attached.feedback.Text, view.attached.feedback.Text)
	}
	assertAppServerSettingsRequest(t, wire, "thread/settings/update", map[string]any{"threadId": "side", "effort": "high"})
}
func TestNativeSidePickerWaitsForCatalog(t *testing.T) {
	u, _, view := nativeSideSettingsTest(t)
	u.modelsLoading = true
	appServerTestRequest(t, u, "99", "model/list")
	requestEditor := new(composer.Editor)
	requestEditor.Text = "/model"
	if handled, err := settingsCommand(&view.attached, view.shell, &u.Client, &view.picker, &requestEditor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort}, &u.serviceTiers, u.title, u.backendVersion, false, u.side, u.clock, &u.questions, &u.approvals, false, u.ownedImages, u.panes, u.models, u.modelsLoading, "/model"); err != nil || !handled {
		t.Fatalf("settings command handled=%v err=%v", handled, err)
	}
	appServerTestMessage(t, u, `{"id":99,"result":{"data":[{"model":"test","defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}]}}`)
	if view.picker.Loading || len(view.picker.Choices) != 1 {
		t.Fatalf("unchanged native Side picker never refreshed: loading=%v choices=%v", view.picker.Loading, view.picker.Choices)
	}
}

func TestSettingsCommandRetainsInputThread(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, wire, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"},"model":"test","reasoningEffort":"low"}`)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	captured := u.attached.presentation.View()
	selectSideThread(t, u, captured.SideThread)
	u.Text = "/model intended-for-main"
	intent := sessionpresentation.ComposerIntent{Version: captured.Version, Thread: captured.Input.ID, Operation: sessionpresentation.SubmitComposer}
	if _, err := u.applyComposerInput(terminalsession.ComposerInput{Command: intent}); err != nil {
		t.Fatal(err)
	}
	if wire.Len() == 0 {
		return
	} // Explicit stale-target rejection also preserves isolation.
	assertAppServerSettingsRequest(t, wire, "thread/settings/update", map[string]any{"threadId": "main", "model": "intended-for-main"})
}

func TestDeferredSideReasoningRetainsTurn(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.model, u.reasoningEffort = "test", "low"
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, wire, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"},"model":"test","reasoningEffort":"low"}`)
	appServerTestKeys(t, u, "old work\r")
	start := btwTestRequest(t, wire, "turn/start", "side")
	btwTestReply(t, u, start, `{"turn":{"id":"old-turn"}}`)
	u.modelsLoading = true
	appServerTestRequest(t, u, "99", "model/list")
	appServerTestKeys(t, u, "\x1b[1;2A")
	if u.attached.reasoning == nil {
		t.Fatal("shortcut not waiting for catalog")
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "old-turn", "status": "completed"}})
	appServerTestKeys(t, u, "new work\r")
	start = btwTestRequest(t, wire, "turn/start", "side")
	btwTestReply(t, u, start, `{"turn":{"id":"new-turn"}}`)
	appServerTestMessage(t, u, `{"id":99,"result":{"data":[{"model":"test","defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}]}}`)
	assertAppServerSettingsRequest(t, wire, "thread/settings/update", map[string]any{"threadId": "side", "effort": "high"})
	appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "side", "threadSettings": map[string]any{"model": "test", "effort": "high"}})
	if wire.Len() != 0 {
		t.Fatalf("deferred shortcut repaired a later turn: %s", wire.String())
	}
}
