package launch

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestUISnapshotNativeChildRuntimeInput(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	main, _ := newAppServerTestUI()
	child, _ := newAppServerTestUI()
	child.Thread = "child"
	main.startSession("main", t.TempDir())
	child.startSession("child", t.TempDir())
	main.model, main.reasoningEffort, main.serviceTier = "parent-only-model", "high", "priority"
	child.model, child.reasoningEffort, child.serviceTier = "child-model", "low", "flex"
	child.Startup.Prepared = &sessioncontrol.PreparedThread{Parent: "main"}
	child.attached.threadInput = &threadInput{admitted: true, caller: "/root/worker", role: "worker"}
	captures := changes.Publication{}.With(changes.GroupCaptures([]changes.Chunk{{Key: "child-change", Workspace: child.attached.cwd, CaptureOrder: 1,
		Origin: changes.Origin{Caller: "/root/worker", Change: "child1", Source: "apply_patch"},
		Review: diffmodel.RenderReviewFile("", filepath.Join(child.attached.cwd, "child.go"), "", "package child\n// captured child edit\n")}}))
	child.reviewSource.PublishReview(child.attached.session.Scope(), &changes.Snapshot{Publication: captures}, child.attached.cwd, "", "")
	frames := make(map[string]sessionapi.NativeFrame)
	for _, u := range []*appServerUI{main, child} {
		u.attached.presentation.Attach(sessionpresentation.Thread{ID: u.Thread, Caller: "/root"}, false)
		sessionpresentation.ApplyActivity(u.attached.source, u.attached.childSource, nil, u.attached.session.Roster())
		u.clock = func() time.Time { return now }
		u.attached.presentation.Status.Text = "Ready"
		u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
		u.testMainFrame(100, 12, 0)
		frames[u.Thread] = sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source), Review: new(u.reviewSource.ReviewView(u.attached.session.Scope()))}
	}
	submitted := make(chan sessionapi.NativeRequest, 1)
	details := make(chan sessionapi.NativeRequest, 4)
	release := make(chan string, 1)
	var hub *sessionapi.Hub
	launcher := &nativeReattachLauncher{
		prepare: func(_ context.Context, _ string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
			return sessionapi.NewPreparedInput(input.NativeDraft.Clone(), nil), nil
		},
		run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
			frame := frames[start.Resume]
			caller, role := "/root", ""
			members := []control.Participant{{Thread: "main", Task: "/root"}, {Thread: "child", Parent: "main", Task: "/root/worker", Role: "worker"}}
			if start.Resume == "child" {
				caller, role = "/root/worker", "worker"
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
					if request.Operation.Composer != nil {
						submitted <- request
					}
					frame.Family, frame.Review = hub.Family(start.Resume)
					frame.View.Version.Revision++
					if err := api.Native().Publish(request.Attachment, frame); err != nil {
						request.Reply <- err.Error()
						continue
					}
					refusal := ""
					if request.Operation.Composer != nil {
						select {
						case refusal = <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					request.Reply <- refusal
				}
			}
		},
	}
	hub = sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: main.attached.cwd, Model: "fixture", Resume: "main"})
	if _, err := hub.StartPrepared(t.Context(), "main", "child", child.attached.cwd, func(ctx context.Context, api *sessionapi.Server) error {
		return launcher.run(ctx, sessionapi.Start{Resume: "child", Workspace: child.attached.cwd}, api)
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
	if err != nil {
		t.Fatal(err)
	}
	runtime := nativeSchedulingRuntime(t, main, connection)
	parent := runtime.active
	parent.editor("main").InsertText("Main draft stays here")
	parent.details(parent.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeSkillDetails, Agent: true, Caller: "/root/worker"})
	if parent.reattach != "child" {
		t.Fatalf("child navigation did not select runtime: %+v", parent.event.Frame.Family.Threads)
	}
	for runtime.active == parent {
		nativeSchedulingStep(t, runtime)
		if runtime.active == parent && !parent.attaching && parent.reattach == "" {
			t.Fatalf("child attachment refused: %s", parent.feedback.Text)
		}
	}
	selected := runtime.active
	for len(details) == 0 {
		nativeSchedulingStep(t, runtime)
	}
	read := <-details
	if read.Thread != "child" || read.Workspace != child.attached.cwd || read.Operation.Details.Agent || read.Operation.Details.Caller != "Main" || read.Operation.Details.Displayed.ID != "child" || parent.attachOperation != nil {
		t.Fatalf("detail did not use the selected runtime's ordinary attachment: %+v", read)
	}
	for !parent.closed {
		nativeSchedulingStep(t, runtime)
	}
	if selected.event.Frame.View.Main.ID != "child" || selected.editor("main") == parent.editor("main") {
		t.Fatal("child did not acquire its own runtime and draft")
	}
	for _, char := range "child input" {
		selected.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: string(char)})
	}
	if selected.editor("main").Text != "child input" || parent.editor("main").Text != "Main draft stays here" {
		t.Fatal("typing crossed thread editors")
	}
	terminal := selected.renderer.terminal
	view := selected.renderer.frame.View
	view.Info.Workspace = "/workspace/child"
	terminal.Main.Update(view)
	painted := terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true}, 100, 8, 0)
	plain := ansi.Strip(strings.Join(painted.Rows, "\n"))
	if !strings.Contains(plain, "worker • child-model (low) • flex") || strings.Contains(plain, "Read-only") || strings.Contains(plain, "parent-only-model") {
		t.Fatalf("child runtime caption/input: %s", plain)
	}
	assertNativeUISnapshot(t, "native-child-runtime-input", painted.Rows)
	rosterRows := terminal.Agents.NativeRoster(100, 8, now, false)
	if text := ansi.Strip(strings.Join(rosterRows, "\n")); !strings.Contains(text, "+2") {
		t.Fatalf("child capture totals missing from native family roster: %s", text)
	}
	diff, err := terminal.Diff.Paint(t.Context(), terminal.Auxiliary, 100, 12)
	if err != nil {
		t.Fatal(err)
	}
	if text := ansi.Strip(strings.Join(diff, "\n")); !strings.Contains(text, "captured child edit") || strings.Contains(text, child.attached.cwd) {
		t.Fatalf("selected child diff missing: %s", text)
	}
	assertNativeUISnapshot(t, "native-child-runtime-diff", diff)
	assertNativeUISnapshot(t, "native-child-runtime-roster", rosterRows)
	selected.event.Frame.View.Caption = sessionpresentation.Caption{}
	selected.refresh(now)
	unknown := terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true}, 100, 8, 0)
	if strings.Contains(strings.Join(unknown.Rows, "\n"), "parent-only-model") || selected.renderer.frame.View.Caption.Model != "" || selected.renderer.frame.View.Caption.Effort != "" || selected.renderer.frame.View.Caption.Tier != "" {
		t.Fatal("missing child settings borrowed Main defaults")
	}
	selected.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\r"})
	for len(submitted) == 0 {
		nativeSchedulingStep(t, runtime)
	}
	request := <-submitted
	if request.Thread != "child" || request.Workspace != child.attached.cwd || request.Operation.Composer.Intent.Thread != "child" || request.Operation.Composer.Draft.Text != "child input" {
		t.Fatalf("submission lost child destination: %+v", request)
	}
	selected.details(selected.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Displayed: parent.event.Frame.View.Main, Path: "main.md"})
	for runtime.active == selected {
		nativeSchedulingStep(t, runtime)
	}
	returned := runtime.active
	if returned == parent || selected.closed {
		t.Fatal("navigation retired an unsettled child input")
	}
	for len(details) == 0 {
		nativeSchedulingStep(t, runtime)
	}
	read = <-details
	if read.Thread != "main" || read.Workspace != main.attached.cwd || read.Operation.Details.Path != "main.md" || read.Operation.Details.Agent {
		t.Fatalf("returning detail did not use Main's ordinary attachment: %+v", read)
	}
	release <- "known child refusal"
	for !selected.effectsSettled() || selected.editor("main").Text == "" {
		nativeSchedulingStep(t, runtime)
	}
	if returned.editor("main").Text != "Main draft stays here" || selected.editor("main").Text != "child input" {
		t.Fatal("child refusal restored into Main")
	}
	returned.displayThread(sessionpresentation.Thread{ID: "child"})
	for runtime.active == returned {
		nativeSchedulingStep(t, runtime)
	}
	if runtime.active.editor("main").Text != "child input" {
		t.Fatal("returning to child lost its draft")
	}
	runtime.close()

	t.Run("PTY", func(t *testing.T) {
		connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		master, slave, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer master.Close()
		defer slave.Close()
		if err := pty.Setsize(master, &pty.Winsize{Cols: 100, Rows: 24}); err != nil {
			t.Fatal(err)
		}
		chunks := readPTYChunks(t.Context(), master, 8192, 64)
		var pending synchronizedFrameBuffer
		terminal, _ := main.terminalViews.NewAttachment(diffrender.DarkTheme)
		client := nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: main.terminalViews}}
		done := make(chan error, 1)
		go func() { _, err := client.run(t.Context(), slave, slave); done <- err }()
		screen := vt.NewEmulator(100, 24)
		repliesDone := make(chan struct{})
		go func() { defer close(repliesDone); _, _ = io.Copy(master, screen) }()
		defer func() { _ = screen.InputPipe().(io.Closer).Close(); <-repliesDone; _ = screen.Close() }()
		await := func(text string) {
			t.Helper()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				for {
					frame, ok := pending.Next()
					if !ok {
						break
					}
					if _, err := screen.Write(frame); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(screen.String(), text) {
						return
					}
				}
				select {
				case chunk, ok := <-chunks:
					if !ok {
						t.Fatal("PTY closed before expected frame")
					}
					pending.Append(chunk)
				case err := <-done:
					t.Fatalf("client exited early: %v", err)
				case <-timer.C:
					t.Fatalf("PTY did not show %q:\n%s", text, screen.String())
				}
			}
		}
		write := func(keys string) {
			t.Helper()
			if _, err := master.WriteString(keys); err != nil {
				t.Fatal(err)
			}
		}
		selectChild := func() {
			t.Helper()
			for row, line := range strings.Split(screen.String(), "\n") {
				if column := strings.Index(line, "worker"); column >= 0 {
					x, y := ansi.StringWidth(line[:column])+1, row+1
					write(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y, x, y))
					return
				}
			}
			t.Fatalf("PTY did not render the admitted child roster row:\n%s", screen.String())
		}
		await("parent-only-model")
		write("PTY Main draft")
		await("PTY Main draft")
		write("\x022")
		await("No visible changes")
		footer := strings.Split(screen.String(), "\n")[23]
		column := strings.Index(footer, "3 Agents")
		if column < 0 {
			t.Fatal("Agents selector missing from terminal")
		}
		x := ansi.StringWidth(footer[:column]) + 1
		write(fmt.Sprintf("\x1b[<0;%d;24M\x1b[<0;%d;24m\x1b[B", x, x))
		await("child-model")
		await("child.go")
		write("\x1b[A")
		await("PTY Main draft")
		await("No visible changes")
		write("\x023\x1b[B")
		await("child-model")
		await("child.go")
		write("\x022")
		write("PTY child input")
		await("PTY child input")
		write("\r")
		select {
		case request := <-submitted:
			if request.Thread != "child" || request.Operation.Composer.Draft.Text != "PTY child input" {
				t.Fatalf("PTY sent to wrong owner: %+v", request)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PTY did not submit child input")
		}
		write("\x021")
		await("PTY Main draft")
		release <- "known child refusal"
		await("known child refusal")
		selectChild()
		await("PTY child input")
		write("\x03/quit\r")
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PTY did not detach")
		}
	})
}

func TestPreparedChildStartupPreservesObservationAudience(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.audience = sessioncontrol.NewAudience(nil)
	u.Startup.Thread = "child"
	u.Startup.Prepared = &sessioncontrol.PreparedThread{Parent: "main"}
	appServerTestRequest(t, u, "1", "thread/resume")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"child","cwd":"/child","turns":[]}}}`)
	if !u.audience.Observe("child", "main", "/root/worker", true) {
		t.Fatal("startup conflicted with admitted child identity")
	}
	u.audience.ObservePrepared("child", activity.PreparedContent{ID: "exec", Text: "model-visible input"})
	if content, ok := u.audience.Prepared("child", "child", "exec"); !ok || content.Text != "model-visible input" {
		t.Fatal("child runtime lost model-visible tool input")
	}
	u.audience.BeginResponse("child", true, time.Unix(1, 0))
	if starts := u.audience.TakeStarts("child"); len(starts) != 1 {
		t.Fatalf("child runtime lost generation tracking: %+v", starts)
	}
}

func TestChildFamilyRosterPreservesMainSummary(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("child", t.TempDir())
	u.testMainFrame(100, 24, 0)
	rootAgent := activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root", Thread: "main"}, Summary: []activity.Entry{{Facts: activity.Facts{Kind: "text", Text: "PARENT_SUMMARY", Agent: "Main"}, Values: []activity.Value{{Kind: "text", Body: "PARENT_SUMMARY"}}}}}
	family := &sessionapi.Family{Root: "main", Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: rootAgent}}, MainSummary: rootAgent.Summary}}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.event.Frame = sessionapi.NativeFrame{Family: family, View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: &activity.Publication{MainSummary: []activity.Entry{{Facts: activity.Facts{Kind: "text", Text: "CHILD_SUMMARY", Agent: "Main"}, Values: []activity.Value{{Kind: "text", Body: "CHILD_SUMMARY"}}}}}}
	c.event.Frame.View.Info.Caller = "/root/worker"
	c.refreshView(time.Now())
	summary, _ := terminal.Agents.Current(rootAgent, time.Now(), 100)
	if !strings.Contains(ansi.Strip(summary), "PARENT_SUMMARY") {
		t.Fatalf("family main row lost its owner: %s", ansi.Strip(summary))
	}
	rootAgent.Summary = nil
	c.event.Frame.Family = &sessionapi.Family{Root: "main", Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: rootAgent}}}, Status: sessionpresentation.Label{Text: "Parent working"}}
	c.refreshView(time.Now())
	summary, _ = terminal.Agents.Current(rootAgent, time.Now(), 100)
	if !strings.Contains(ansi.Strip(summary), "Parent working") {
		t.Fatalf("family Main status borrowed child state: %s", summary)
	}

}

func TestChildFamilyRosterPreservesEditCounts(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("child", t.TempDir())
	u.testMainFrame(120, 24, 0)
	rootAgent := activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root", Thread: "main"}}
	childAgent := activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/worker", Thread: "child"}}
	roster := &activity.Publication{Roster: []activity.AgentRow{{Agent: rootAgent}, {Agent: childAgent, Depth: 1}}, LineCounts: map[string]diffmodel.Counts{"/root": {Added: 100}, "/root/worker": {Added: 3}}, NetCounts: new(diffmodel.Counts{Added: 103})}
	family := &sessionapi.Family{Root: "main", Agents: roster}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.event.Frame = sessionapi.NativeFrame{Family: family, View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: &activity.Publication{LineCounts: map[string]diffmodel.Counts{"/root": {Added: 3}}, NetCounts: new(diffmodel.Counts{Added: 3})}}
	c.event.Frame.View.Info.Caller = "/root/worker"
	c.refreshView(time.Now())
	rows := ansi.Strip(strings.Join(terminal.Agents.NativeRoster(120, 10, time.Now(), false), "\n"))
	if !strings.Contains(rows, "+100") || !strings.Contains(rows, "+103") {
		t.Fatalf("selected child changed family roster edit ownership:\n%s", rows)
	}
}

func TestPreparedChildStartsWithItsOwnReviewWorkspace(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Startup.Prepared = &sessioncontrol.PreparedThread{Parent: "main"}
	appServerTestRequest(t, u, "1", "thread/start")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"child","cwd":"/var/tmp/child-worktree","turns":[]}}}`)
	view := u.reviewSource.ReviewView(u.attached.session.Scope())
	if view.Workspace != "/var/tmp/child-worktree" || !u.review.Scope().Workspaces[view.Workspace]["child"] {
		t.Fatalf("fresh child borrowed another review workspace: %+v", view)
	}
}

// Publish the same ordinary source consumed by Hub.Family, independently of any
// native attachment or selected renderer.
func publishNativeFamilySource(api *sessionapi.Server, frame sessionapi.NativeFrame, workspace, root, caller, role string, members []control.Participant) error {
	return api.Publish(sessionapi.Frame{
		Session:    sessionapi.Session{Thread: frame.View.Main.ID, Workspace: workspace, Caller: caller, Role: role},
		FamilyRoot: root,
		Status:     frame.View.Status.State, Members: members,
		Main: frame.Main, Agents: frame.Agents, Review: frame.Review,
	})
}
