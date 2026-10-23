package launch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
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
		publishSessionView(&view.attached, &u.Client, &view.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
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
	view := publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	if view.Main.ID != "" || view.Info.Workspace != "/workspace" || view.Version.Attachment == 0 {
		t.Fatalf("startup publication fabricated a confirmed thread: %+v", view)
	}
}
