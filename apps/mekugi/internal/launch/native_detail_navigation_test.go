package launch

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeDetailNavigationSiblingAndFailedAttachment(t *testing.T) {
	main, _ := newAppServerTestUI()
	main.startSession("main", t.TempDir())
	threads := []observation.Thread{
		{ID: "first", Path: "/root/first", Role: "worker"},
		{ID: "sibling", Path: "/root/sibling", Role: "worker"},
		{ID: "unavailable", Path: "/root/unavailable", Role: "worker"},
	}
	frames := make(map[string]sessionapi.NativeFrame)
	workspaces := map[string]string{"main": main.attached.cwd}
	members := []control.Participant{{Thread: "main", Task: "/root"}}
	for _, thread := range threads {
		members = append(members, control.Participant{Thread: thread.ID, Parent: "main", Task: thread.Path, Role: thread.Role})
		u, _ := newAppServerTestUI()
		u.Thread = thread.ID
		u.startSession(thread.ID, t.TempDir())
		u.attached.presentation.Attach(sessionpresentation.Thread{ID: thread.ID, Caller: "/root"}, false)
		u.testMainFrame(80, 24, 0)
		frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil)}
		frames[thread.ID], workspaces[thread.ID] = frame, u.attached.cwd
	}
	main.attached.presentation.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, false)
	sessionpresentation.ApplyActivity(main.attached.source, main.attached.childSource, nil, main.attached.session.Roster())
	main.testMainFrame(80, 24, 0)
	parentFrame := sessionapi.NativeFrame{View: main.attached.presentation.View(), Main: main.attached.source.Publication(main.attached.presentation, nil), Agents: main.attached.childSource.Publication(main.attached.presentation, main.attached.source)}
	frames["main"] = parentFrame
	details := make(chan sessionapi.NativeRequest, 8)
	var hub *sessionapi.Hub
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		frame := frames[start.Resume]
		caller, role := "/root", ""
		if start.Resume != "main" {
			caller, role = "/root/"+start.Resume, "worker"
		}
		if err := publishNativeFamilySource(api, frame, start.Workspace, "main", caller, role, members); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: start.Workspace, Ready: true}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case request := <-api.Native().Requests():
				if request.Operation.Details != nil {
					details <- request
				}
				frame.Family, frame.Review = hub.Family(start.Resume)
				frame.View.Version.Revision++
				if err := api.Native().Publish(request.Attachment, frame); err != nil {
					request.Reply <- err.Error()
				} else {
					request.Reply <- ""
				}
			}
		}
	}}
	hub = sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	start := func(thread string) {
		t.Helper()
		if _, err := hub.StartPrepared(t.Context(), "main", thread, workspaces[thread], func(ctx context.Context, api *sessionapi.Server) error {
			return launcher.run(ctx, sessionapi.Start{Resume: thread, Workspace: workspaces[thread]}, api)
		}); err != nil {
			t.Fatal(err)
		}
	}
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspaces["main"], Model: "fixture", Resume: "main"})
	for _, thread := range []string{"first", "sibling"} {
		start(thread)
	}
	connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
	if err != nil {
		t.Fatal(err)
	}
	r := nativeSchedulingRuntime(t, main, connection)
	parent := r.active
	parent.displayThread(sessionpresentation.Thread{ID: "first"})
	for r.active == parent {
		nativeSchedulingStep(t, r)
	}
	for _, thread := range []string{"sibling"} {
		source := r.active
		stale := source.event.Frame.View.Version
		stale.Attachment++
		queued := len(source.calls)
		source.details(stale, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Displayed: sessionpresentation.Thread{ID: thread}, Path: "stale-click.go"})
		if source.reattach != "" || source.attachOperation != nil || len(source.calls) != queued || source.notice != sessionpresentation.ErrStaleIntent.Error() {
			t.Fatal("stale source click acquired a destination attachment")
		}
		source.details(source.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Agent: true, Displayed: sessionpresentation.Thread{ID: thread}, Path: thread + ".go"})
		for len(details) == 0 {
			nativeSchedulingStep(t, r)
		}
		request := <-details
		if r.active == source || request.Thread != thread || request.Workspace != workspaces[thread] || request.Attachment != r.active.event.Attachment || request.Operation.Details.Agent || request.Operation.Details.Displayed.ID != thread || request.Operation.Details.Path != thread+".go" {
			t.Fatalf("detail did not acquire destination attachment for %s: %+v", thread, request)
		}
	}
	source := r.active
	source.editor("main").InsertText("retain source draft")
	source.details(source.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Displayed: sessionpresentation.Thread{ID: "unavailable"}, Path: "stale.go"})
	for source.reattach != "" || source.attaching {
		nativeSchedulingStep(t, r)
	}
	if r.active != source || source.closed || source.attachOperation != nil || source.feedback.Text == "" || source.editor("main").Text != "retain source draft" || len(details) != 0 {
		t.Fatal("failed attachment lost the source or retained its pending detail")
	}
	start("unavailable")
	source.displayThread(sessionpresentation.Thread{ID: "unavailable"})
	for r.active == source {
		nativeSchedulingStep(t, r)
	}
	selected := r.active
	selected.details(selected.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "fresh.go"})
	for len(details) == 0 {
		nativeSchedulingStep(t, r)
	}
	if request := <-details; request.Thread != "unavailable" || request.Operation.Details.Path != "fresh.go" {
		t.Fatalf("failed attachment replayed stale details: %+v", request)
	}
}

func TestNativeDetailUnknownSkillCallerIsStale(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.attached.session.RegisterThread(observation.Thread{ID: "child", Path: "/root/child"})
	sessionpresentation.ApplyActivity(u.attached.source, u.attached.childSource, nil, u.attached.session.Roster())
	for _, roster := range []bool{false, true} {
		c := &nativeClient{event: sessionapi.NativeEvent{Frame: sessionapi.NativeFrame{View: u.attached.presentation.View()}}}
		if roster {
			terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
			terminal.Agents.UpdateRoster(u.attached.childSource.Publication(u.attached.presentation, u.attached.source))
			c.renderer.terminal = terminal
		}
		c.details(c.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeSkillDetails, Agent: true, Caller: "/root/gone"})
		if len(c.calls) != 0 || c.reattach != "" || c.attachOperation != nil || c.notice != sessionpresentation.ErrStaleIntent.Error() {
			t.Fatalf("missing skill caller became a local detail, roster=%v", roster)
		}
	}
}

func TestNativeDetailWorkspaceAdmissionAndLocalFiles(t *testing.T) {
	parent, _ := newAppServerTestUI()
	parent.startSession("main", t.TempDir())
	child, _ := newAppServerTestUI()
	child.Thread = "child"
	child.startSession("child", t.TempDir())
	for _, u := range []*appServerUI{parent, child} {
		if err := os.WriteFile(filepath.Join(u.attached.cwd, "same.go"), []byte(u.Thread+" content\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: "main", Workspace: start.Workspace, Caller: "/root"}, FamilyRoot: "main", Members: []control.Participant{{Thread: "main", Task: "/root"}, {Thread: "child", Parent: "main", Task: "/root/child"}}}); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: "main", Workspace: start.Workspace, Ready: true}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: parent.attached.cwd, Model: "fixture", Resume: "main"})
	if _, err := hub.StartPrepared(t.Context(), "main", "", child.attached.cwd, func(ctx context.Context, api *sessionapi.Server) error {
		if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: "child", Workspace: child.attached.cwd, Caller: "/root/child"}, FamilyRoot: "main", Members: []control.Participant{{Thread: "main", Task: "/root"}, {Thread: "child", Parent: "main", Task: "/root/child"}}}); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: "child", Workspace: child.attached.cwd, Ready: true}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if family, _ := hub.Family("child"); family == nil || len(family.Threads) != 2 {
		t.Fatal("child runtime was not published in its ordinary family")
	}
	n, view, _ := nativeDetailTestViews(t, parent)
	n.threads = &threadExecution{hub: hub}
	target := sessionpresentation.Thread{ID: "child", Caller: "/root/child"}
	operation := sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Displayed: target, Agent: true, Path: "same.go"}}
	for _, displayed := range []bool{false, true} {
		if displayed {
			if err := view.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: view.attached.presentation.View().Version, Thread: target}); err != nil {
				t.Fatal(err)
			}
		}
		request := sessionapi.NativeRequest{Context: t.Context(), Attachment: view.attached.view, Thread: "main", Workspace: parent.attached.cwd, Version: view.attached.presentation.View().Version, Operation: operation}
		if _, refusal := n.admit(request, &parent.attached, "main"); refusal == "" || view.files.fileReads != nil {
			t.Fatalf("live child detail admitted on parent attachment, displayed=%v", displayed)
		}
		request.Operation = sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{Kind: sessionapi.NativeSkillDetails, Agent: true, Caller: "/root/child"}}
		if _, refusal := n.admit(request, &parent.attached, "main"); refusal == "" {
			t.Fatal("skill caller bypassed live destination admission")
		}
	}
	drain := func(v *nativeSessionView) {
		t.Helper()
		deadline := time.After(time.Second)
		for v.files.fileReads != nil {
			select {
			case result := <-v.files.fileReads:
				v.files.loadedMarkdownFile(&v.attached.session, v.attached.presentation, v.attached.cwd, v.shell, result)
			case <-deadline:
				t.Fatal("local detail acquisition stalled")
			}
		}
	}
	_, local, _ := nativeDetailTestViews(t, child)
	if refusal := local.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "same.go"}); refusal != "" {
		t.Fatal(refusal)
	}
	drain(local)
	if frame := nativeDetailFrame(local); frame.Details == nil || frame.Details.File == nil || frame.Details.Source.ID != "child" || frame.Details.File.Source != "child content\n" {
		t.Fatalf("child local detail borrowed parent content: %+v", frame.Details)
	}
	if refusal := local.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "."}); refusal != "" {
		t.Fatal(refusal)
	}
	drain(local)
	effects := local.effects.publication()
	if len(effects) != 1 || effects[0].Thread != "child" || effects[0].Clipboard == nil || *effects[0].Clipboard != child.attached.cwd || nativeDetailFrame(local).Details != nil {
		t.Fatalf("directory copy lost destination workspace: %+v", effects)
	}
	for _, id := range []string{"historical", "other"} {
		parent.attached.session.RegisterThread(observation.Thread{ID: id, Path: "/root/" + id})
	}
	target = sessionpresentation.Thread{ID: "historical", Caller: "/root/historical"}
	operation.Details.Displayed = target
	request := sessionapi.NativeRequest{Context: t.Context(), Attachment: view.attached.view, Thread: "main", Workspace: parent.attached.cwd, Version: view.attached.presentation.View().Version, Operation: operation}
	// Browser selection is local; a captured historical source also survives
	// later server-side navigation to a different historical thread.
	for _, displayed := range []sessionpresentation.Thread{view.attached.presentation.View().Main, {ID: "other", Caller: "/root/other"}} {
		if err := view.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: view.attached.presentation.View().Version, Thread: displayed}); err != nil {
			t.Fatal(err)
		}
		admitted, refusal := n.admit(request, &parent.attached, "main")
		if refusal != "" {
			t.Fatal(refusal)
		}
		if refusal := admitted.applyView(operation); refusal != "" {
			t.Fatal(refusal)
		}
		drain(view)
		if frame := nativeDetailFrame(view); frame.Details == nil || frame.Details.File == nil || frame.Details.File.Source != "main content\n" {
			t.Fatal("captured historical local details became unavailable")
		}
	}
}
