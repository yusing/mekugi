package launch

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func nativeDetailTestViews(t *testing.T, u *appServerUI) (*nativeViews, *nativeSessionView, *nativeSessionView) {
	t.Helper()
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	t.Cleanup(n.close)
	for _, id := range []string{"first", "second"} {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		if refusal := n.attach(sessionapi.NativeRequest{Attach: true, Attachment: id, Thread: u.Thread, Workspace: u.attached.cwd, Context: ctx, ViewContext: ctx}, &u.attached, u.Thread); refusal != "" {
			t.Fatal(refusal)
		}
		v := n.views[id]
		publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	}
	return n, n.views["first"], n.views["second"]
}

func nativeDetailFrame(v *nativeSessionView) sessionapi.NativeFrame {
	frame := sessionapi.NativeFrame{View: v.attached.presentation.View()}
	v.publishDetails(&frame)
	return frame
}

func TestNativeDetailsSelectAndCloseIndependently(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	firstOutput := u.activity.Outputs(u.attached.session.Scope()).New()
	secondOutput := u.activity.Outputs(u.attached.session.Scope()).New()
	firstOutput.Finish(new("first output\n"), new(0))
	secondOutput.Finish(new("second output\n"), new(0))
	seq := u.attached.source.LastSeq + 1
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: seq, Agent: "Main", Kind: "tool", Text: "Run `first`", Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: "first", Output: firstOutput}},
		{Seq: seq + 1, Agent: "Main", Kind: "tool", Text: "Run `second`", Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: "second", Output: secondOutput}},
	}})
	n, first, second := nativeDetailTestViews(t, u)
	for i, view := range []*nativeSessionView{first, second} {
		before := view.attached.presentation.View()
		client := nativeClient{event: sessionapi.NativeEvent{Attachment: view.attached.view, Frame: sessionapi.NativeFrame{View: before}}}
		client.details(before.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq + uint64(i)})
		call := client.calls[0]
		request := sessionapi.NativeRequest{Attachment: call.target.Attachment, Thread: call.target.Thread, Version: call.target.Version, Workspace: u.attached.cwd, Context: t.Context(), Operation: call.operation}
		view.attached.feedback.Set("running progress", false, time.Now())
		after := publishSessionView(&view.attached, &u.Client, &view.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
		if after.Version == before.Version {
			t.Fatal("fixture did not advance the running publication")
		}
		admitted, refusal := n.admit(request, &u.attached, u.Thread)
		if refusal != "" {
			t.Fatal(refusal)
		}
		if refusal := admitted.applyView(request.Operation); refusal != "" {
			t.Fatal(refusal)
		}
		request.Workspace = "/replacement"
		if _, refusal := n.admit(request, &u.attached, u.Thread); refusal == "" {
			t.Fatal("detail request crossed workspace identity")
		}
		request.Workspace = u.attached.cwd
		request.Version.Attachment++
		if _, refusal := n.admit(request, &u.attached, u.Thread); refusal == "" {
			t.Fatal("detail request crossed attachment identity")
		}
		request.Version = before.Version
		model := view.attached.presentation
		if err := model.Display(sessionpresentation.DisplayIntent{Version: model.View().Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/child"}}); err != nil {
			t.Fatal(err)
		}
		if _, refusal := n.admit(request, &u.attached, u.Thread); refusal != "" {
			t.Fatalf("navigation invalidated the original detail source: %s", refusal)
		}
		if refusal := view.applyView(request.Operation); refusal != "" {
			t.Fatal(refusal)
		}
		if err := model.Display(sessionpresentation.DisplayIntent{Version: model.View().Version, Thread: before.Displayed}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := nativeDetailFrame(first), nativeDetailFrame(second)
	if a.Details.Source != first.attached.presentation.View().Main || b.Details.Source != second.attached.presentation.View().Main {
		t.Fatal("detail publication lost its source runtime")
	}
	if a.Details == nil || b.Details == nil || a.Details.View.ID == b.Details.View.ID || a.Details.Output == nil || b.Details.Output == nil {
		t.Fatal("views shared or lost the selected detail/output")
	}
	if strings.Join(a.Details.Output.View.Lines, "\n") != "first output" || strings.Join(b.Details.Output.View.Lines, "\n") != "second output" {
		t.Fatal("detail outputs crossed clients")
	}
	foreign := a.Details.Output.DetailOutputRequest
	if refusal := second.applyView(sessionapi.NativeOperation{DetailOutput: &foreign}); refusal == "" {
		t.Fatal("foreign detail selection hydrated another view")
	}
	id := a.Details.View.ID
	if refusal := first.applyView(sessionapi.NativeOperation{CloseDetails: &id}); refusal != "" {
		t.Fatal(refusal)
	}
	if nativeDetailFrame(first).Details != nil || nativeDetailFrame(second).Details.View.ID != b.Details.View.ID {
		t.Fatal("closing one detail changed another selection")
	}
	n.retire("first")
	if nativeDetailFrame(second).Details.Output == nil || firstOutput.View().Released || secondOutput.View().Released || u.attached.session.Scope() == nil {
		t.Fatal("view disposal released shared execution/output ownership")
	}
}

func TestNativeFileDialogsOwnReadAndStaleCompletionLifetime(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	for name, text := range map[string]string{"first.go": "package first\n", "second.go": "package second\n"} {
		if err := os.WriteFile(filepath.Join(u.attached.cwd, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	n, first, second := nativeDetailTestViews(t, u)
	first.openNoticeDetails("See first.go")
	before := first.attached.presentation.View()
	client := nativeClient{event: sessionapi.NativeEvent{Attachment: "first", Frame: sessionapi.NativeFrame{View: before}}, renderer: nativeClientRenderer{terminal: first.shell}}
	client.dialog(terminalsession.DialogIntent{Action: terminalsession.DialogOpenLink, Text: "first.go"})
	first.attached.feedback.Set("progress before link admission", false, time.Now())
	after := publishSessionView(&first.attached, &u.Client, &first.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	if after.Version == before.Version || len(client.calls) != 1 {
		t.Fatal("fixture did not capture a link before publication advanced")
	}
	call := client.calls[0]
	request := sessionapi.NativeRequest{Attachment: call.target.Attachment, Thread: call.target.Thread, Version: call.target.Version, Workspace: u.attached.cwd, Context: t.Context(), Operation: call.operation}
	if _, refusal := n.admit(request, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	if refusal := first.applyView(request.Operation); refusal != "" {
		t.Fatal(refusal)
	}
	if refusal := second.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "second.go"}); refusal != "" {
		t.Fatal(refusal)
	}
	firstResults, secondResults := first.files.fileReads, second.files.fileReads
	if firstResults == secondResults || first.files.fileReadSlot == second.files.fileReadSlot {
		t.Fatal("native views shared acquisition or capacity")
	}
	old := first.shell.Dialog.ID
	if refusal := first.openNoticeDetails("replacement detail"); refusal != "" {
		t.Fatal(refusal)
	}
	select {
	case stale := <-firstResults:
		if first.files.loadedMarkdownFile(&first.attached.session, first.attached.presentation, first.attached.cwd, first.shell, stale) {
			t.Fatal("stale file completion replaced a new dialog")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled acquisition did not finish")
	}
	if first.shell.Dialog.ID == old {
		t.Fatal("replacement reused a detail identity")
	}
	n.retire("first")
	before = second.attached.presentation.View()
	if err := second.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: before.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/child"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-secondResults:
		if !second.files.loadedMarkdownFile(&second.attached.session, second.attached.presentation, second.attached.cwd, second.shell, result) {
			t.Fatal("closing first view canceled second read")
		}
	case <-time.After(time.Second):
		t.Fatal("second acquisition did not finish")
	}
	frame := nativeDetailFrame(second)
	if frame.Details == nil || frame.Details.File == nil || frame.Details.File.Source != "package second\n" || frame.Details.File.Path != "second.go" {
		t.Fatal("file publication lost selected workspace/content")
	}
}

func TestNativeProcessDetailsBorrowExecutionCommands(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	_, view, _ := nativeDetailTestViews(t, u)
	view.files.openDetailSelection(&view.attached.session, view.attached.presentation.View().Version, view.attached.childSource, view.shell, view.shell.Agents, nil, true)
	processNotify(t, u, "main", "process", "printf running", "inProgress")
	frame := nativeDetailFrame(view)
	if frame.Details == nil || !frame.Details.View.Processes || len(frame.Details.View.Pages) != 1 || frame.Details.View.Pages[0].Code != "printf running" {
		t.Fatal("process dialog read a copied or empty command owner")
	}
}

func TestNativeDetailInitialPageDoesNotOverrideClientNavigation(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "Main", Kind: "error", Text: "First failure"},
		{Seq: 2, Agent: "Main", Kind: "error", Text: "Newest failure"},
	}})
	_, view, _ := nativeDetailTestViews(t, u)
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeErrorDetails}); refusal != "" {
		t.Fatal(refusal)
	}
	frame := nativeDetailFrame(view)
	if frame.Details == nil || len(frame.Details.View.Pages) != 2 || frame.Details.Selected != frame.Details.View.Pages[1].ID {
		t.Fatal("new error dialog lost newest-page selection")
	}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	renderer := nativeClientRenderer{terminal: terminal, views: u.terminalViews}
	renderer.update(frame, conversation.DraftView{Available: true}, conversation.PromptDraftView{}, time.Now())
	if renderer.terminal.Dialog.Page != 1 {
		t.Fatal("native client did not use initial selected page identity")
	}
	renderer.terminal.Dialog.ShowPage(0)
	renderer.update(frame, conversation.DraftView{Available: true}, conversation.PromptDraftView{}, time.Now())
	if renderer.terminal.Dialog.Page != 0 {
		t.Fatal("publication overrode renderer-local page navigation")
	}
}

func TestNativeChildDetailsKeepCapturedSourceAcrossNavigation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	u, _ := newAppServerTestUI()
	workspace := t.TempDir()
	u.startSession("main", workspace)
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.SetSummary(sessionapi.Summary{Thread: "main", Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	hub := sessionapi.NewHub(ctx, launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(ctx, server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"})
	reads := make(chan sessionapi.NativeRequest, 8)
	for _, thread := range []string{"child-a", "child-b"} {
		u.attached.session.RegisterThread(observation.Thread{ID: thread, Path: "/root/" + thread})
		_, err := hub.StartPrepared(ctx, "main", "", workspace, func(ctx context.Context, api *sessionapi.Server) error {
			if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: thread, Workspace: workspace}, Main: &activity.Publication{}}); err != nil {
				return err
			}
			if err := api.SetSummary(sessionapi.Summary{Thread: thread, Workspace: workspace, Ready: true}); err != nil {
				return err
			}
			frames := make(map[string]sessionapi.NativeFrame)
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case request := <-api.Native().Requests():
					frame := frames[request.Attachment]
					if request.Attach {
						frame.View = sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Main: sessionpresentation.Thread{ID: thread}, Displayed: sessionpresentation.Thread{ID: thread}}
						frame.View.Info.Workspace = workspace
					}
					if details := request.Operation.Details; details != nil {
						if details.Displayed.ID != thread {
							t.Errorf("detail source %q reached %q", details.Displayed.ID, thread)
						}
						frame.Details = &sessionapi.NativeDetails{Source: frame.View.Main, View: sessionpresentation.DetailView{ID: sessionpresentation.DetailID{Attachment: 1, Selection: 1}, Pages: []sessionpresentation.DetailItem{{DetailPage: activity.DetailPage{ID: thread}}}}}
						reads <- request
					}
					if request.Operation.DetailOutput != nil {
						reads <- request
					}
					frames[request.Attachment] = frame
					request.Reply <- ""
					if err := api.Native().Publish(request.Attachment, frame); err != nil {
						return err
					}
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	n, view, _ := nativeDetailTestViews(t, u)
	view.children = hub
	view.child, err = hub.Observe(ctx, "main", "child-b")
	if err != nil {
		t.Fatal(err)
	}
	view.childThread = "child-b"
	if err := view.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: view.attached.presentation.View().Version, Thread: sessionpresentation.Thread{ID: "child-b", Caller: "/root/child-b"}}); err != nil {
		t.Fatal(err)
	}
	// A main-source file remains local even while a live child is displayed.
	path := filepath.Join(workspace, "main.md")
	if err := os.WriteFile(path, []byte("main source"), 0600); err != nil {
		t.Fatal(err)
	}
	if refusal := view.applyView(sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{Displayed: sessionpresentation.Thread{ID: "main"}, Kind: sessionapi.NativeFileDetails, Path: path}}); refusal != "" {
		t.Fatal(refusal)
	}
	if view.childDetails != nil || view.shell.Dialog == nil {
		t.Fatal("main details followed the displayed child")
	}
	open := func(thread string) sessionapi.NativeDetails {
		t.Helper()
		operation := sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{Displayed: sessionpresentation.Thread{ID: thread}, Kind: sessionapi.NativeEntryDetails, Source: 7, Agent: true}}
		request := sessionapi.NativeRequest{Context: ctx, Attachment: view.attached.view, Version: view.attached.presentation.View().Version, Thread: "main", Workspace: workspace, Operation: operation}
		admitted, refusal := n.admit(request, &u.attached, "main")
		if refusal != "" {
			t.Fatal(refusal)
		}
		if refusal := admitted.applyView(operation); refusal != "" {
			t.Fatal(refusal)
		}
		select {
		case read := <-reads:
			if read.Thread != thread {
				t.Fatalf("captured %s reached %s", thread, read.Thread)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		for {
			frame := nativeDetailFrame(view)
			if frame.Details != nil {
				return *frame.Details
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	first := open("child-a")
	observer := view.childDetails.observation
	if refusal := view.applyView(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: view.attached.presentation.View().Version, Thread: sessionpresentation.Thread{ID: "main"}}}); refusal != "" {
		t.Fatal(refusal)
	}
	if frame := nativeDetailFrame(view); frame.Details == nil || frame.Details.View.ID != first.View.ID {
		t.Fatal("navigation closed the captured child dialog")
	}
	if refusal := view.applyView(sessionapi.NativeOperation{DetailOutput: &sessionpresentation.DetailOutputRequest{ID: first.View.ID, Page: "child-a", Version: 1}}); refusal != "" {
		t.Fatal(refusal)
	}
	select {
	case read := <-reads:
		if read.Thread != "child-a" || read.Operation.DetailOutput.ID.Selection != 1 {
			t.Fatal("hydration lost its child owner")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := open("child-b")
	if second.View.ID == first.View.ID {
		t.Fatal("replacement reused the parent's detail identity")
	}
	if err := observer.Read(sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{}}); err == nil {
		t.Fatal("replaced observation remained open")
	}
	if refusal := view.applyView(sessionapi.NativeOperation{CloseDetails: &first.View.ID}); refusal == "" {
		t.Fatal("stale close reached the replacement dialog")
	}
	if refusal := view.applyView(sessionapi.NativeOperation{CloseDetails: &second.View.ID}); refusal != "" {
		t.Fatal(refusal)
	}
	if nativeDetailFrame(view).Details != nil {
		t.Fatal("closed child details remained published")
	}
}

func TestNativeChildDialogLinkKeepsSourceAcrossNavigation(t *testing.T) {
	for _, destination := range []string{"main", "child-b"} {
		t.Run(destination, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/parent-workspace")
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			t.Cleanup(func() { _ = terminal.Diff.Close() })
			c := nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			view := u.attached.presentation.View()
			view.Displayed = sessionpresentation.Thread{ID: "child-a", Caller: "/root/child-a"}
			view.DisplayedWorkspace = "/child-workspace"
			detail := &sessionapi.NativeDetails{Source: view.Displayed, Agent: true, View: sessionpresentation.DetailView{
				ID:    sessionpresentation.DetailID{Attachment: view.Version.Attachment, Selection: 1},
				Pages: []sessionpresentation.DetailItem{{DetailPage: activity.DetailPage{ID: "child-page"}}},
			}}
			event := sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: view, Details: detail}}
			c.accept(event)
			event.Frame.View.Displayed = sessionpresentation.Thread{ID: destination}
			event.Frame.View.DisplayedWorkspace = "/other-workspace"
			c.accept(event)
			c.dialog(terminalsession.DialogIntent{Action: terminalsession.DialogOpenLink, Text: "source.go"})
			if len(c.calls) != 1 || c.calls[0].operation.Details == nil {
				t.Fatalf("missing file detail request: %+v", c.calls)
			}
			request := c.calls[0].operation.Details
			if request.Displayed != detail.Source || request.Path != "source.go" {
				t.Fatalf("retained dialog link changed source: %+v", request)
			}
		})
	}
}

func TestNativeChildProjectionRetainsImmutableSnapshots(t *testing.T) {
	n := &nativeViews{threads: &threadExecution{}}
	v := &nativeSessionView{}
	base := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Thread: "child", Name: "/root/worker", Role: "worker"}}}}
	project := func(children map[string]sessionapi.Descendant) *activity.Publication {
		frame := sessionapi.NativeFrame{Agents: base, View: sessionpresentation.View{Main: sessionpresentation.Thread{ID: "main"}, Displayed: sessionpresentation.Thread{ID: "main"}}}
		n.publishChild(v, &frame, children)
		return frame.Agents
	}
	if got := project(nil); got != base {
		t.Fatal("a view without child publications copied its immutable feed")
	}
	own := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Thread: "child", Name: "/root", Status: "working", Summary: []activity.Entry{{Agent: "Main", Text: "retained"}}}}}}
	first := project(map[string]sessionapi.Descendant{"child": {Agents: own}})
	if first == base || first.Roster[0].Agent.Status != "working" || first.Roster[0].Agent.Name != "/root/worker" || first.Roster[0].Agent.Summary[0].Agent != "/root/worker" {
		t.Fatal("child roster projection lost owner facts or caller labels")
	}
	if got := project(map[string]sessionapi.Descendant{"child": {Agents: own, Caption: sessionpresentation.Caption{Model: "changed"}}}); got != first {
		t.Fatal("unchanged Activity snapshots replaced the projected feed")
	}
	next := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Thread: "child", Name: "/root", Status: "idle"}}}}
	second := project(map[string]sessionapi.Descendant{"child": {Agents: next}})
	if second == first || second.Roster[0].Agent.Status != "idle" || first.Roster[0].Agent.Status != "working" || base.Roster[0].Agent.Status != "" {
		t.Fatal("changed child snapshot mutated or reused its predecessor")
	}
	if got := project(nil); got != base {
		t.Fatal("retired child retained a stale projected feed")
	}
}
