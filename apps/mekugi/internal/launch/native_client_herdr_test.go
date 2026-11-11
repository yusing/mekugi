package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/herdr"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func herdrFrame(t *testing.T) (sessionapi.NativeFrame, terminalsession.Views) {
	t.Helper()
	u, _ := newAppServerTestUI()
	u.attached.cwd = "/workspace/project"
	u.attached.presentation.Status.Text = "Ready"
	u.settings.Model = "fixture"
	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "final", Text: "Native attachment transcript.", Observed: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)}}})
	u.testMainFrame(120, 24, 0)
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root", Thread: "main"}}}, {Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/child", Thread: "child"}}, Depth: 1}, {Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/foreign", Thread: "foreign", NonAttachable: true}}, Depth: 1}}}}
	frame.Main = u.attached.source.Publication(u.attached.presentation, nil)
	frame.View.Main = sessionpresentation.Thread{ID: "main", Caller: "/root"}
	frame.View.Displayed = frame.View.Main
	frame.View.Input = frame.View.Main
	frame.Family = &sessionapi.Family{Root: "main", Threads: []string{"main", "child"}, Agents: frame.Agents}
	file := changes.File{Path: "a.go", Chunks: []changes.Chunk{{Origin: changes.Origin{Caller: "/root"}, Review: diffmodel.RenderReviewFile("a.go", "a.go", "old\n", "new\n")}}}
	review := changes.View{Files: []changes.File{file}}
	review.RefreshVisible()
	frame.Review = &changes.ReviewView{Publication: changes.Publication{Files: review.Files, Unfiltered: review.Visible}, Revision: 1, Workspace: u.attached.cwd, Coverage: "complete"}
	frame.Live = []preview.Preview{{ID: "live", Thread: "main", Caller: "/root", Workspace: u.attached.cwd, DiffText: true, Input: "--- live.go\n+++ live.go\n@@ -1 +1 @@\n-old live\n+new live\n", Status: preview.PreviewEdit}}
	frame.LiveScope = preview.Scope{Workspaces: map[string]map[string]bool{u.attached.cwd: {"main": true, "child": true}}}
	return frame, u.terminalViews
}

func TestNativeHerdrUnchangedHeaderSkipsPublication(t *testing.T) {
	header := sessionpresentation.FrontendHeader{Displayed: sessionpresentation.Thread{ID: "main"}, Name: "Main", Skills: []string{"commit", "plane"}, Above: 10000}
	client := &nativeClient{connection: &sessionapi.NativeConnection{}, frontendActive: true, renderer: nativeClientRenderer{header: &header}}
	runtime := nativeClientRuntime{active: client, headerSent: &header}
	if allocations := testing.AllocsPerRun(10000, func() { runtime.publishHeader(client) }); allocations != 0 {
		t.Fatalf("unchanged header allocated %g times per frame", allocations)
	}
	if runtime.headerPending || runtime.headerRevision != 0 {
		t.Fatal("unchanged frames dispatched an auxiliary request")
	}
}

func TestUISnapshotNativeHerdrLease(t *testing.T) {
	frame, views := herdrFrame(t)
	terminal, _ := views.NewAttachment(diffrender.DarkTheme)
	terminal.Width, terminal.Height = 120, 24
	t.Cleanup(func() { terminal.Diff.Close() })
	renderer := nativeClientRenderer{terminal: terminal, views: views}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	renderer.update(frame, conversation.DraftView{Draft: composer.Draft{Text: "Keep this draft."}}, conversation.PromptDraftView{}, now)
	terminal.Focus, terminal.DiffOpen, terminal.Split = 3, true, 62
	before := terminal.Shell.Preferences(terminal.Main)
	terminal.SetExternalPanes(true)
	// Neither keyboard selector can reopen a duplicate while the native lease owns it.
	for _, key := range []string{"\x02", "2", "\x02", "3"} {
		for range terminal.FrameInput(now, "Mekugi fixture").Dispatch(&terminal.Interaction, terminalsession.Input{Kind: terminalsession.KeyInput, Text: key}) {
		}
	}
	for _, phase := range []string{"active", "expired"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "expired" {
				terminal.SetExternalPanes(false)
			}
			var out bytes.Buffer
			painted, err := renderer.render(t.Context(), &out, now.Add(2*time.Second), "Mekugi fixture")
			if err != nil {
				t.Fatal(err)
			}
			if phase == "active" && (painted.Source.Thread != "main" || terminal.Layout.Diff.W != 0 || terminal.Layout.Roster.H != 0 || terminal.Layout.Live.H != 0 || terminal.Focus != 0) {
				t.Fatalf("duplicate panes remain: %+v", terminal.Layout)
			}
			if after := terminal.Shell.Preferences(terminal.Main); before != after {
				t.Fatalf("lease persisted renderer override: before=%+v after=%+v", before, after)
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/native-herdr-"+phase+".txt", append(painted.Rows, "plain after native bridge"), 120)
		})
	}
	if terminal.Layout.Diff.W == 0 || terminal.Layout.Roster.H == 0 || terminal.Layout.Live.H == 0 || terminal.Focus != 3 {
		t.Fatalf("expiry did not restore panes: %+v", terminal.Layout)
	}
}

func TestUISnapshotNativeHerdrCompanionSizes(t *testing.T) {
	frame, views := herdrFrame(t)
	for _, test := range []struct {
		name          string
		width, height int
	}{{"narrow", 36, 24}, {"short", 80, 7}} {
		t.Run(test.name, func(t *testing.T) {
			terminal, _ := views.NewAttachment(diffrender.DarkTheme)
			terminal.Width, terminal.Height = test.width, test.height
			t.Cleanup(func() { _ = terminal.Diff.Close() })
			renderer := nativeClientRenderer{terminal: terminal, views: views}
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			renderer.update(frame, conversation.DraftView{Draft: composer.Draft{Text: "Keep this draft."}}, conversation.PromptDraftView{}, now)
			terminal.SetExternalPanes(true)
			var out bytes.Buffer
			painted, err := renderer.render(t.Context(), &out, now.Add(2*time.Second), "Mekugi fixture")
			if err != nil {
				t.Fatal(err)
			}
			if terminal.Layout.Main != (terminalsession.Rect{W: test.width, H: test.height}) {
				t.Fatalf("companion did not reclaim terminal area: %+v", terminal.Layout.Main)
			}
			screen := vt.NewEmulator(test.width, test.height)
			defer screen.Close()
			_, _ = screen.Write(out.Bytes())
			if visible := screen.Render(); !strings.Contains(visible, "Keep this draft.") || !strings.Contains(visible, "Ready") || !strings.Contains(visible, "fixture") {
				t.Fatalf("draft, status or caption lost: %s", visible)
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/native-herdr-companion-"+test.name+".txt", append(painted.Rows, "plain after native companion"), test.width)
		})
	}
}

func TestNativeHerdrSharedHelperPTYDraftsAndRelease(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	dir, err := os.MkdirTemp("/tmp", "mh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	base, views := herdrFrame(t)
	base.View.Info.Title = "Review native app session metadata"
	base.View.Info.Skills = activity.SkillSet{"commit", "plane"}
	base.Main.Entries = nil
	for seq := uint64(1); seq <= 740; seq++ {
		base.Main.Entries = append(base.Main.Entries, activity.Entry{Facts: activity.Facts{Seq: seq, Agent: "Main", Kind: "text", Text: fmt.Sprintf("Retained transcript line %d.", seq)}, Revision: 1})
	}
	base.Main.LastSeq = 740
	viewerRequests := make(chan struct{}, 64)
	base.View.Info.Workspace = dir
	base.Review.Workspace = dir
	base.LiveScope = preview.Scope{Workspaces: map[string]map[string]bool{dir: {"main": true, "child": true}}}
	for index := range base.Live {
		base.Live[index].Workspace = dir
	}
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		frame := base
		frame.View.Main = sessionpresentation.Thread{ID: start.Resume, Caller: "/root"}
		if start.Resume == "child" {
			frame.View.Main.Caller = "/root/child"
		}
		frame.View.Info.Caller = frame.View.Main.Caller
		frame.View.Displayed, frame.View.Input = frame.View.Main, frame.View.Main
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: dir, Ready: true}); err != nil {
			return err
		}
		if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: start.Resume, Caller: frame.View.Main.Caller, Workspace: dir}, Attachment: 1, Agents: frame.Agents, Review: frame.Review, Live: frame.Live}); err != nil {
			return err
		}
		u, _ := newAppServerTestUI()
		u.Thread = start.Resume
		u.startSession(start.Resume, dir)
		owners := map[string]*nativeSessionView{}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case request := <-api.Native().Requests():
				owner := owners[request.Attachment]
				if owner == nil {
					_, owner, _ = nativeDetailTestViews(t, u)
					owners[request.Attachment] = owner
				}
				switch {
				case request.Operation.Viewer != nil:
					owner.viewerLease = *request.Operation.Viewer
					owner.effects.add(sessionapi.NativeEffect{Thread: start.Resume, Presentation: &sessionapi.NativePresentationEffect{Viewer: request.Operation.Viewer}})
					viewerRequests <- struct{}{}
				case request.Operation.Details != nil:
					if refusal := owner.openDetails(*request.Operation.Details); refusal != "" {
						request.Reply <- refusal
						continue
					}
					awaitNativeFileDialog(t, owner)
				case request.Operation.Navigate != nil:
					if refusal := owner.selectAgent(*request.Operation.Navigate, frame.Family.Agents); refusal != "" {
						request.Reply <- refusal
						continue
					}
				case request.Operation.Receipt != nil:
					owner.effects.acknowledge(request.Operation.Receipt.Effect, false)
				}
				current := frame
				owner.publishDetails(&current)
				current.Effects = owner.effects.publication()
				if err := api.Native().Publish(request.Attachment, current); err != nil {
					request.Reply <- err.Error()
				} else {
					request.Reply <- ""
				}
			}
		}
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	path, err := sessionapi.ControlSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := sessionapi.ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: sessionapi.Versioned(hub.Handler())}
	defer server.Close()
	go server.Serve(listener)
	transport := sessionapi.ControlTransport(path)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	rpc, err := sessionapi.OpenRPC(t.Context(), client, "http://mekugi"+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	for _, thread := range []string{"main", "child"} {
		serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: dir, Model: "fixture", Resume: thread})
	}
	connection, err := sessionapi.OpenNative(t.Context(), client, "http://mekugi", "main")
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
	if err = pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 36}); err != nil {
		t.Fatal(err)
	}
	terminal, _ := views.NewAttachment(diffrender.DarkTheme)
	terminal.DiffOpen, terminal.Split = true, 60
	native := nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: views}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := native.run(ctx, slave, slave); done <- err }()
	frames := make(chan []byte, 128)
	go func() {
		var pending synchronizedFrameBuffer
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			pending.Append(buffer[:n])
			for {
				frame, ok := pending.Next()
				if !ok {
					break
				}
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(120, 36)
	repliesDone := make(chan struct{})
	go func() { defer close(repliesDone); _, _ = io.Copy(master, screen) }()
	defer func() { _ = screen.InputPipe().(io.Closer).Close(); <-repliesDone; _ = screen.Close() }()
	await := func(match func(string) bool) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case frame := <-frames:
				_, _ = screen.Write(frame)
				if match(screen.String()) {
					return
				}
			case err := <-done:
				t.Fatalf("native client exited: %v", err)
			case <-timer.C:
				t.Fatalf("terminal condition unmet:\n%s", screen.String())
			}
		}
	}
	request := func(req herdr.Request) (sessionapi.NativeObservation, int) {
		t.Helper()
		data, _ := json.Marshal(req)
		var out, diagnostic bytes.Buffer
		code := herdr.Run(ctx, []string{"--pid", fmt.Sprint(os.Getpid()), "--request", string(data)}, &out, &diagnostic)
		var result sessionapi.NativeObservation
		if code == 0 {
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Logf("helper refused: %s", &diagnostic)
		}
		return result, code
	}
	body := func(value sessionapi.NativeFrontendRequest) jsontext.Value {
		data, _ := json.Marshal(value)
		return data
	}
	type observationResult struct {
		value sessionapi.NativeObservation
		code  int
	}
	startObserve := func(previous sessionapi.NativeObservation) <-chan observationResult {
		t.Helper()
		for len(viewerRequests) > 0 {
			<-viewerRequests
		}
		result := make(chan observationResult, 1)
		go func() {
			value, code := request(herdr.Request{Method: "native/observe", Instance: previous.Instance, Body: body(sessionapi.NativeFrontendRequest{Viewer: "pty", After: previous.Cursor, Attachment: previous.Attachment})})
			result <- observationResult{value, code}
		}()
		select {
		case <-viewerRequests:
		case <-time.After(5 * time.Second):
			t.Fatal("follow-up observation not admitted")
		}
		return result
	}
	finishObserve := func(result <-chan observationResult, thread string) sessionapi.NativeObservation {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case observed := <-result:
				if observed.code != 0 || !observed.value.Available {
					t.Fatalf("automatic observer handoff: code=%d thread=%q activation=%d", observed.code, observed.value.Thread, observed.value.Activation)
				}
				if observed.value.Thread == thread {
					return observed.value
				}
				// A painted-header update can precede the requested thread switch.
				result = startObserve(observed.value)
			case <-timer.C:
				t.Fatal("observer did not follow completed native handoff")
			}
		}
	}
	await(func(s string) bool { return strings.Contains(s, "2 Diff") && strings.Contains(s, "new live") })
	assertCompanionFrame := func() {
		t.Helper()
		if top := strings.Split(screen.String(), "\n")[0]; strings.Contains(top, "┌") || strings.Contains(top, "1 Main") {
			t.Fatalf("native companion did not reclaim top frame row: %q", top)
		}
	}
	if !strings.Contains(strings.Split(screen.String(), "\n")[0], "1 Main") {
		t.Fatal("standalone terminal lost its frame before companion observation")
	}
	if _, err = master.WriteString("parent draft"); err != nil {
		t.Fatal(err)
	}
	await(func(s string) bool { return strings.Contains(s, "parent draft") })
	initial, code := request(herdr.Request{Method: "native/observe", Body: body(sessionapi.NativeFrontendRequest{Viewer: "pty"})})
	if code != 0 || !initial.Available || initial.Thread != "main" || !initial.FileOpenSupported {
		t.Fatalf("initial shared observation: %+v code=%d", initial, code)
	}
	await(func(s string) bool {
		return !strings.Contains(s, "2 Diff") && !strings.Contains(s, "3 Agents") && !strings.Contains(s, "new live") && strings.Contains(s, "parent draft")
	})
	assertCompanionFrame()
	if initial.Header == nil || initial.Header.Above == 0 {
		initial = finishObserve(startObserve(initial), "main")
	}
	if initial.Header == nil || initial.Header.Name != "Main" || initial.Header.Title != base.View.Info.Title || strings.Join(initial.Header.Skills, ",") != "commit,plane" || initial.Header.Above <= 0 {
		t.Fatalf("painted header facts did not reach companion: %+v", initial.Header)
	}
	file := filepath.Join(dir, "source with spaces.go")
	initial, code = request(herdr.Request{Method: "native/observe", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Viewer: "pty", FileOpen: true})})
	if code != 0 {
		t.Fatal("file-opening lease failed")
	}
	if err = os.WriteFile(file, []byte("first\nsecond\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for id := uint64(1); id <= 2; id++ {
		_, err = connection.Send(ctx, sessionapi.NativeTarget{Thread: initial.Thread, Attachment: initial.Attachment, Version: initial.Version}, sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: file + ":2-3"}})
		if err != nil {
			t.Fatal(err)
		}
		opened, status := request(herdr.Request{Method: "native/observe", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Viewer: "pty", FileOpen: true, After: initial.Cursor})})
		want := sessionapi.NativeFileOpen{ID: id, Path: file, First: 2, Last: 3}
		if status != 0 || opened.FileOpen == nil || *opened.FileOpen != want || opened.Attachment != initial.Attachment {
			t.Fatalf("line-qualified file action through helper: %+v code=%d", opened, status)
		}
		initial = opened
	}
	for _, agent := range []string{"/root/foreign", "/root/missing"} {
		if _, code = request(herdr.Request{Method: "native/select", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Activation: initial.Activation, Attachment: initial.Attachment, Displayed: initial.Displayed.ID, Agent: agent})}); code == 0 {
			t.Fatalf("unattachable agent %q admitted", agent)
		}
	}
	watchChild := startObserve(initial)
	if _, code = request(herdr.Request{Method: "native/select", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Activation: initial.Activation, Attachment: initial.Attachment, Displayed: initial.Displayed.ID, Agent: "/root/child"})}); code != 0 {
		t.Fatal("child navigation failed")
	}
	await(func(s string) bool { return !strings.Contains(s, "parent draft") && !strings.Contains(s, "2 Diff") })
	if _, err = master.WriteString("child draft"); err != nil {
		t.Fatal(err)
	}
	await(func(s string) bool { return strings.Contains(s, "child draft") })
	child := finishObserve(watchChild, "child")
	if child.FileOpen != nil {
		t.Fatal("file selection crossed attachment handoff")
	}
	if child.Displayed.Caller != "/root/child" {
		t.Fatalf("child displayed identity: %+v", child)
	}
	raced, code := request(herdr.Request{Method: "native/observe", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Viewer: "pty", After: initial.Cursor, Attachment: initial.Attachment})})
	if code != 0 || raced.Thread != "child" {
		t.Fatal("former observation attachment required manual retry")
	}
	watchParent := startObserve(child)
	if _, code = request(herdr.Request{Method: "native/select", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Activation: child.Activation, Attachment: child.Attachment, Displayed: child.Displayed.ID, Agent: "/root"})}); code != 0 {
		t.Fatal("return navigation failed")
	}
	await(func(s string) bool { return strings.Contains(s, "parent draft") && !strings.Contains(s, "child draft") })
	returned := finishObserve(watchParent, "main")
	if _, code = request(herdr.Request{Method: "native/release", Instance: initial.Instance, Body: body(sessionapi.NativeFrontendRequest{Attachment: returned.Attachment, Viewer: "pty"})}); code != 0 {
		t.Fatal("release failed")
	}
	await(func(s string) bool {
		return strings.Contains(s, "2 Diff") && strings.Contains(s, "3 Agents") && strings.Contains(s, "parent draft") && strings.Contains(s, "new live")
	})
	if !strings.Contains(strings.Split(screen.String(), "\n")[0], "1 Main") {
		t.Fatal("companion release did not restore standalone framing")
	}
	if ready := os.Getenv("MEKUGI_TEST_HERDR_FIXTURE_READY"); ready != "" {
		data, _ := json.Marshal(struct {
			PID    int    `json:"pid"`
			Cache  string `json:"cache"`
			Socket string `json:"socket"`
		}{os.Getpid(), dir, path})
		if err := os.WriteFile(ready, data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("shared helper fixture ready, pid=%d HOME=%s", os.Getpid(), dir)
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		waiting := true
		for waiting {
			select {
			case <-ctx.Done():
				waiting = false
			case <-timer.C:
				waiting = false
			case frame := <-frames:
				_, _ = screen.Write(frame)
			}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("native client did not close")
	}
}

func TestNativeViewerDelayedEffectsAndNavigationGeneration(t *testing.T) {
	frame, views := herdrFrame(t)
	makeClient := func() *nativeClient {
		terminal, _ := views.NewAttachment(diffrender.DarkTheme)
		terminal.Width, terminal.Height = 120, 24
		t.Cleanup(func() { _ = terminal.Diff.Close() })
		return &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: views}}
	}
	first, second := makeClient(), makeClient()
	runtime := &nativeClientRuntime{active: first, activation: 3, editors: map[[2]string]*composer.Editor{}}
	first.runtime, second.runtime = runtime, runtime
	first.accept(sessionapi.NativeEvent{Attachment: "first", Frame: frame})
	childFrame := frame
	childFrame.View.Main = sessionpresentation.Thread{ID: "child", Caller: "/root/child"}
	childFrame.View.Displayed = childFrame.View.Main
	second.accept(sessionapi.NativeEvent{Attachment: "second", Frame: childFrame})
	first.editor("main").Text = "preserve first draft"
	second.editor("main").Text = "preserve second draft"
	deadline := time.Now().Add(10 * time.Second)
	publish := func(client *nativeClient, id uint64, effect sessionapi.NativeEffect) {
		current := frame
		effect.ID = id
		current.Effects = []sessionapi.NativeEffect{effect}
		attachment := "first"
		if client == second {
			attachment = "second"
			current = childFrame
			current.Effects = []sessionapi.NativeEffect{effect}
		}
		client.accept(sessionapi.NativeEvent{Attachment: attachment, Frame: current})
	}
	publish(first, 1, sessionapi.NativeEffect{Presentation: &sessionapi.NativePresentationEffect{Viewer: &sessionapi.NativeViewerLease{Revision: 3, Expires: deadline.UnixMilli()}}})
	if runtime.viewerExpiry.After(deadline) || !runtime.viewerExpiry.After(time.Now()) {
		t.Fatal("delivery restarted lease instead of applying remaining lifetime")
	}
	original := runtime.viewerExpiry
	runtime.active = second
	// A newer release from the retained old stream still applies to the physical
	// terminal, while an older renewal on the new stream cannot restore hiding.
	publish(first, 2, sessionapi.NativeEffect{Presentation: &sessionapi.NativePresentationEffect{Viewer: &sessionapi.NativeViewerLease{Revision: 5}}})
	publish(second, 1, sessionapi.NativeEffect{Presentation: &sessionapi.NativePresentationEffect{Viewer: &sessionapi.NativeViewerLease{Revision: 4, Expires: deadline.Add(time.Minute).UnixMilli()}}})
	if !runtime.viewerExpiry.IsZero() || runtime.viewerRevision != 5 {
		t.Fatal("cross-runtime delivery reversed release ordering")
	}
	publish(second, 2, sessionapi.NativeEffect{Presentation: &sessionapi.NativePresentationEffect{Viewer: &sessionapi.NativeViewerLease{Revision: 6, Expires: time.Now().Add(-time.Second).UnixMilli()}}})
	if runtime.viewerExpiry.After(time.Now()) || original.IsZero() {
		t.Fatal("delayed expired lease renewed suppression")
	}
	stale := &sessionapi.NativeNavigation{Activation: 2, Displayed: childFrame.View.Displayed.ID, Thread: sessionpresentation.Thread{ID: "main", Caller: "/root"}}
	publish(second, 3, sessionapi.NativeEffect{Navigate: stale})
	if second.reattach != "" {
		t.Fatal("stale activation navigated after handoff")
	}
	fresh := *stale
	fresh.Activation = 3
	publish(second, 4, sessionapi.NativeEffect{Navigate: &fresh})
	if second.reattach != "main" {
		t.Fatal("admitted navigation bypassed displayThread")
	}
	if first.editor("main").Text != "preserve first draft" || second.editor("main").Text != "preserve second draft" {
		t.Fatal("presentation effects consumed local drafts")
	}
}
