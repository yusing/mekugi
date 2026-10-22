package launch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// Drive the same coordinator as the PTY client while controlling exactly when
// the server publishes a hydration result or finishes input acquisition.
func nativeSchedulingStep(t *testing.T, r *nativeClientRuntime) {
	t.Helper()
	r.schedule()
	select {
	case message := <-r.messages:
		if err := r.deliver(message); err != nil {
			t.Fatal(err)
		}
	case call := <-r.results:
		call.complete = true
	case result := <-r.reattached:
		r.reattach(result, nil)
	case <-time.After(5 * time.Second):
		t.Fatal("native client scheduling stalled")
	}
	r.settle()
	for _, owner := range r.owners {
		owner.drainInput()
	}
	r.retire()
}

func nativeSchedulingRuntime(t *testing.T, u *appServerUI, connection *sessionapi.NativeConnection) *nativeClientRuntime {
	t.Helper()
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	terminal.Width, terminal.Height = 80, 24
	c := &nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	r := &nativeClientRuntime{ctx: t.Context(), active: c, messages: make(chan nativeClientMessage), results: make(chan *nativeClientCall, nativeCallLanes), reattached: make(chan nativeClientReattached, 1), editors: make(map[[2]string]*composer.Editor), images: make(map[string]bool)}
	r.start(c, nil)
	t.Cleanup(r.close)
	for c.event.Attachment == "" {
		nativeSchedulingStep(t, r)
	}
	return r
}

func TestNativeClientHydrationDoesNotHoldControl(t *testing.T) {
	u, _ := newAppServerTestUI()
	workspace := t.TempDir()
	u.startSession("main", workspace)
	u.testMainFrame(80, 24, 0)
	requests := make(chan sessionapi.NativeRequest, 32)
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil)}
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case request := <-api.Native().Requests():
				if request.Operation.Output != nil {
					// The read acknowledges without delivering its output frame.
					request.Reply <- ""
					requests <- request
					continue
				}
				frame.View.Version.Revision++
				_ = api.Native().Publish(request.Attachment, frame)
				request.Reply <- ""
				requests <- request
			}
		}
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
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture", Resume: "main"})
	connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
	if err != nil {
		t.Fatal(err)
	}
	r := nativeSchedulingRuntime(t, u, connection)
	c := r.active
	c.enqueue(sessionapi.NativeOperation{Output: &sessionapi.NativeOutputDemand{Demands: []activity.OutputDemand{{Source: 1, Output: 1, Version: 1}}}}, nil, nil)
	for c.request(nativeObservationCall) == nil || !c.request(nativeObservationCall).complete {
		nativeSchedulingStep(t, r)
	}
	c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "still editing"})
	c.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: c.event.Frame.View.Version, Thread: c.event.Frame.View.Main}}, nil, nil)
	c.enqueue(sessionapi.NativeOperation{Interrupt: &sessionpresentation.InterruptIntent{Version: c.event.Frame.View.Version, Target: sessioncontrol.InterruptTarget{}}}, nil, nil)
	c.effect = 3
	c.stageReceipt(nil)
	var displayed, interrupted, acknowledged, completed bool
	for !displayed || !interrupted || !acknowledged || !completed {
		nativeSchedulingStep(t, r)
		for draining := true; draining; {
			select {
			case request := <-requests:
				completed = completed || request.Operation.Completion != nil && request.Operation.Completion.Draft.Text == "still editing"
				displayed = displayed || request.Operation.Display != nil
				interrupted = interrupted || request.Operation.Interrupt != nil
				acknowledged = acknowledged || request.Operation.Receipt != nil && request.Operation.Receipt.Effect == 3
			default:
				draining = false
			}
		}
	}
	if c.request(nativeObservationCall) == nil || c.request(nativeObservationCall).hydrated || c.editor("main").Text != "still editing" {
		t.Fatal("hydration blocked or replaced the live editor")
	}
	var painted bytes.Buffer
	if _, err := c.renderer.render(t.Context(), &painted, time.Now(), mekugiVersion()); err != nil || !strings.Contains(painted.String(), "still editing") {
		t.Fatalf("editing was not painted while hydration stalled: %v", err)
	}
}

func TestNativeClientPreparationRetainsOriginalOwnerAcrossNavigation(t *testing.T) {
	for _, test := range []struct{ name, input string }{{"ordinary", "original input"}, {"slash", "/status"}} {
		t.Run(test.name, func(t *testing.T) { testNativeClientPreparationOwner(t, test.input) })
	}
}

func testNativeClientPreparationOwner(t *testing.T, input string) {
	u, _ := newAppServerTestUI()
	workspace := t.TempDir()
	u.startSession("main", workspace)
	u.testMainFrame(80, 24, 0)
	base := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil)}
	preparing, release := make(chan struct{}), make(chan struct{})
	settle := make(chan struct{})
	requests := make(chan sessionapi.NativeRequest, 64)
	launcher := &nativeReattachLauncher{
		prepare: func(ctx context.Context, workspace string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
			close(preparing)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return sessionapi.NewPreparedInput(input.NativeDraft.Clone(), nil), nil
		},
		run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
			frame := base
			frame.View.Main.ID, frame.View.Input.ID, frame.View.Displayed.ID = start.Resume, start.Resume, start.Resume
			if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: workspace, Ready: true}); err != nil {
				return err
			}
			var attachment string
			var finish <-chan struct{}
			if start.Resume == "main" {
				finish = settle
			}
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-finish:
					finish = nil
					frame.PendingEffects = false
					frame.View.Status.Feedback = sessionpresentation.Label{Text: "source input was refused", Alert: true}
					frame.Effects = []sessionapi.NativeEffect{{ID: 1, Thread: "main", Restore: &composer.Draft{Text: input}}}
					frame.View.Version.Revision++
					_ = api.Native().Publish(attachment, frame)
					_ = api.Native().Publish(attachment, frame)
				case request := <-api.Native().Requests():
					attachment = request.Attachment
					if request.Operation.Composer != nil {
						frame.PendingEffects = true
					}
					if display := request.Operation.Display; display != nil {
						frame.View.Input, frame.View.Displayed = display.Thread, display.Thread
					}
					if receipt := request.Operation.Receipt; receipt != nil && receipt.Effect >= 1 {
						frame.Effects = nil
					}
					frame.View.Version.Revision++
					_ = api.Native().Publish(request.Attachment, frame)
					request.Reply <- ""
					requests <- request
				}
			}
		},
	}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	for _, thread := range []string{"main", "destination"} {
		serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture", Resume: thread})
	}
	connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
	if err != nil {
		t.Fatal(err)
	}
	r := nativeSchedulingRuntime(t, u, connection)
	source := r.active
	source.editor("main").InsertText(input)
	_, target := source.selectedEditor()
	source.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: source.event.Frame.View.Version, Thread: "main", Target: target, Operation: sessionpresentation.SubmitComposer}})
	r.schedule()
	select {
	case <-preparing:
	case <-time.After(5 * time.Second):
		t.Fatal("input preparation did not begin")
	}
	source.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "next draft"})
	source.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: source.event.Frame.View.Version, Thread: sessionpresentation.Thread{ID: "side", Caller: "child"}}}, nil, nil)
	source.enqueue(sessionapi.NativeOperation{Interrupt: &sessionpresentation.InterruptIntent{Version: source.event.Frame.View.Version}}, nil, nil)
	var interrupted bool
	for source.event.Frame.View.Input.ID != "side" || !interrupted {
		nativeSchedulingStep(t, r)
		for draining := true; draining; {
			select {
			case request := <-requests:
				interrupted = interrupted || request.Operation.Interrupt != nil
			default:
				draining = false
			}
		}
	}
	source.reattach = "destination"
	for r.active == source {
		nativeSchedulingStep(t, r)
	}
	destination := r.active
	if source.ctx.Err() != nil || destination.editor("main").Text != "" || source.editor("main").Text != "next draft" {
		t.Fatal("navigation retired or retargeted the original input/editor")
	}
	destination.editor("main").InsertText("destination draft")
	_, target = destination.selectedEditor()
	destination.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: destination.event.Frame.View.Version, Thread: "destination", Target: target, Operation: sessionpresentation.SubmitComposer}})
	if destination.editor("main").Text != "destination draft" || destination.request(nativeInputCall) != nil {
		t.Fatal("busy input capacity consumed the destination draft")
	}
	close(release)
	var submitted *sessionapi.NativeRequest
	for submitted == nil || source.request(nativeInputCall) != nil || source.request(nativeSemanticCall) != nil && source.request(nativeSemanticCall).operation.Composer != nil {
		nativeSchedulingStep(t, r)
		for draining := true; draining; {
			select {
			case request := <-requests:
				if request.Operation.Composer != nil {
					if submitted != nil {
						t.Fatal("prepared input was sent more than once")
					}
					submitted = &request
				}
			default:
				draining = false
			}
		}
	}
	if submitted.Thread != "main" || submitted.Operation.Composer.Intent.Thread != "main" || submitted.Prepared.Draft.Text != input || !source.event.Frame.PendingEffects || source.ctx.Err() != nil {
		t.Fatal("prepared input lost its originating target or outcome lifetime")
	}
	close(settle)
	for !source.closed {
		nativeSchedulingStep(t, r)
	}
	if r.active != destination || destination.editor("main").Text != "destination draft" || strings.Count(source.editor("main").Text, input) != 1 || !strings.Contains(source.editor("main").Text, "next draft") {
		t.Fatal("hidden restoration was lost, duplicated, or applied to the destination")
	}
	if !strings.Contains(destination.feedback.Text, "Session main: source input was refused") {
		t.Fatal("hidden source retirement lost its actual refusal")
	}
	destination.reattach = "main"
	for r.active == destination {
		nativeSchedulingStep(t, r)
	}
	if r.active.editor("main") != source.editor("main") {
		t.Fatal("return navigation lost the originating editor")
	}
}

func TestNativeClientContinuationKeepsOriginatingEditor(t *testing.T) {
	for _, destination := range []string{"side", "new-question"} {
		t.Run(destination, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			c.event.Frame.View.Menu = &sessionpresentation.MenuView{ID: 7, Kind: sessionpresentation.MenuSkills}
			c.refresh(time.Now())
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1bx"})
			var pending *nativeClientCall
			for _, call := range c.calls {
				if call.next != nil {
					pending = call
					break
				}
			}
			if pending == nil {
				t.Fatal("menu did not retain the trailing key")
			}
			c.calls = nil
			r := &nativeClientRuntime{active: c, editors: make(map[[2]string]*composer.Editor)}
			c.runtime = r
			r.requests[nativeSemanticCall] = pending
			c.queueInput(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "y"})
			c.drainInput()
			c.event.Frame.View.Menu = nil
			if destination == "side" {
				c.event.Frame.View.Input, c.event.Frame.View.Displayed = sessionpresentation.Thread{ID: "side"}, sessionpresentation.Thread{ID: "side"}
			} else {
				questionTestSync(t, u, "new-question", false)
				c.event.Frame.Questions = nativePendingQuestions(u.attached.conversation)
				c.prompts.update(&c.event.Frame)
				c.openPrompts()
			}
			c.refresh(time.Now())
			pending.complete, pending.result.Version = true, c.event.Frame.View.Version
			r.settle()
			c.drainInput()
			if c.editor("main").Text != "xy" || c.editor("side:side").Text != "" || destination == "side" && c.event.Frame.View.Input.ID != "side" {
				t.Fatal("continuation followed navigation instead of its original editor")
			}
			if destination == "new-question" && c.prompts.questions.Active == nil {
				t.Fatal("continuation dismissed the newly published prompt")
			}
		})
	}
}

func TestNativeClientAcquisitionsFollowOriginalEditorAfterNavigation(t *testing.T) {
	for _, acquisition := range []string{"clipboard", "external"} {
		t.Run(acquisition, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			c.ctx = t.Context()
			original, target := c.selectedEditor()
			c.event.Frame.View.Input, c.event.Frame.View.Displayed = sessionpresentation.Thread{ID: "side"}, sessionpresentation.Thread{ID: "side"}
			c.refresh(time.Now())
			if acquisition == "clipboard" {
				c.ownedImages = make(map[string]bool)
				c.clipboardResult(nativeClipboardResult{stream: c.event.Attachment, attachment: c.event.Frame.View.Version.Attachment, editor: original, target: target, path: pasteTestImage(t)})
				if len(original.Images) != 1 || len(c.editor("side:side").Images) != 0 {
					t.Fatal("clipboard image followed the selected editor")
				}
				return
			}
			script := t.TempDir() + "/editor"
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'external draft' > \"$1\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EDITOR", script)
			c.completionReady = true
			_, c.completionTarget = c.selectedEditor()
			c.completionInput, c.completionCatalog = c.event.Frame.View.Input.ID, c.event.Frame.CompletionCatalog
			r := &nativeClientRuntime{ctx: t.Context(), active: c}
			if err := c.external(t.Context(), nil, nil, &nativeEditorHandoff{editor: original, target: target, attachment: c.event.Frame.View.Version.Attachment}, r); err != nil {
				t.Fatal(err)
			}
			if original.Text != "external draft" || c.editor("side:side").Text != "" {
				t.Fatal("external editor output followed navigation")
			}
		})
	}
}

func TestNativeClientPasteKeepsOriginalEditor(t *testing.T) {
	for _, navigation := range []string{"side", "prompt", "session"} {
		t.Run(navigation, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			source := nativeTestPromptClient(u)
			r := &nativeClientRuntime{active: source, owners: []*nativeClient{source}, editors: make(map[[2]string]*composer.Editor)}
			source.runtime = r
			decode := func(text string) {
				for _, key := range []byte(text) {
					r.decode(key, time.Now())
					for _, owner := range r.owners {
						owner.drainInput()
					}
				}
			}
			decode("\x1b[200~first ")
			switch navigation {
			case "side":
				source.event.Frame.View.Input, source.event.Frame.View.Displayed = sessionpresentation.Thread{ID: "side"}, sessionpresentation.Thread{ID: "side"}
				source.refresh(time.Now())
			case "prompt":
				questionTestSync(t, u, "arrived", false)
				source.event.Frame.Questions = nativePendingQuestions(u.attached.conversation)
				source.prompts.update(&source.event.Frame)
				source.openPrompts()
				source.refresh(time.Now())
			case "session":
				destination := nativeTestPromptClient(u)
				destination.event.Frame.View.Main.ID, destination.event.Frame.View.Input.ID, destination.event.Frame.View.Displayed.ID = "destination", "destination", "destination"
				r.active = destination
				r.owners = append(r.owners, destination)
			}
			decode("second\r/quit\x1b[201~")
			if source.editor("main").Text != "first second\n/quit" || r.pasteOwner != nil || r.active.quit || source.editor("side:side").Text != "" {
				t.Fatalf("paste followed navigation or executed its content: %q", source.editor("main").Text)
			}
			if r.active != source && r.active.editor("main").Text != "" {
				t.Fatal("source paste entered the new session")
			}
		})
	}
}

func TestNativeClientHiddenAnswerConnectionDropRetainsOrigin(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "secret"}[secret], func(t *testing.T) {
			u, _ := newAppServerTestUI()
			workspace := t.TempDir()
			u.startSession("main", workspace)
			questionTestSync(t, u, "question", secret)
			u.testMainFrame(80, 24, 0)
			base := sessionapi.NativeFrame{View: u.attached.presentation.View(), Questions: nativePendingQuestions(u.attached.conversation)}
			answers := make(chan sessionapi.NativeRequest, 2)
			launcher := &nativeReattachLauncher{
				prepare: func(_ context.Context, _ string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
					return sessionapi.NewPreparedInput(input.NativeDraft.Clone(), nil), nil
				},
				run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
					frame := base
					frame.View.Main.ID, frame.View.Input.ID, frame.View.Displayed.ID = start.Resume, start.Resume, start.Resume
					if start.Resume != "main" {
						frame.Questions = nil
					}
					if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: workspace, Ready: true}); err != nil {
						return err
					}
					for {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case request := <-api.Native().Requests():
							if request.Operation.Answers != nil {
								answers <- request // Dispatched, deliberately no admission reply.
								continue
							}
							frame.View.Version.Revision++
							_ = api.Native().Publish(request.Attachment, frame)
							request.Reply <- ""
						}
					}
				},
			}
			hub := sessionapi.NewHub(t.Context(), launcher)
			defer hub.Close()
			server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
			defer server.Close()
			rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
			if err != nil {
				t.Fatal(err)
			}
			defer rpc.Close()
			for _, thread := range []string{"main", "destination"} {
				serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture", Resume: thread})
			}
			connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
			if err != nil {
				t.Fatal(err)
			}
			r := nativeSchedulingRuntime(t, u, connection)
			source := r.active
			if !secret {
				nativeTestPromptKey(t, source, "\x1b[A")
			}
			editor, _ := source.selectedEditor()
			editor.InsertText("originating answer")
			questionID := source.prompts.questions.Current().ID
			nativeTestPromptKey(t, source, "\r")
			r.schedule()
			select {
			case <-answers:
			case <-time.After(5 * time.Second):
				t.Fatal("answer did not reach original session")
			}
			source.reattach = "destination"
			for r.active == source {
				nativeSchedulingStep(t, r)
			}
			destination := r.active
			destination.editor("main").InsertText("destination draft")
			source.connection.Close()
			for !source.closed {
				nativeSchedulingStep(t, r)
			}
			if secret {
				if len(r.unknown) != 0 {
					t.Fatal("secret entered unknown outcome recovery")
				}
			} else if len(r.unknown) != 1 || r.unknown[0].Text != "originating answer" || r.unknown[0].Thread != "main" || r.unknown[0].Prompt != questionID {
				t.Fatalf("unknown answer lost originating identity: %+v", r.unknown)
			}
			if r.active != destination || destination.editor("main").Text != "destination draft" || len(source.editor("main").InputHistory) != 0 || len(destination.editor("main").InputHistory) != 0 {
				t.Fatal("source loss affected destination or labeled unknown input unsent")
			}
			select {
			case <-answers:
				t.Fatal("unknown answer was retried")
			default:
			}
		})
	}
}

func TestNativeClientOutputCatchupReacquiresOnlyUnresolvedIdentities(t *testing.T) {
	u, _ := newAppServerTestUI()
	workspace := t.TempDir()
	u.startSession("main", workspace)
	u.testMainFrame(80, 24, 0)
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: &activity.Publication{}}
	for i := range 3 {
		frame.Main.Entries = append(frame.Main.Entries, activity.Entry{Seq: uint64(i + 1), Agent: "Main", Kind: "tool", Text: "command", Outputs: map[int]int{1: 2}, Values: []activity.Value{{Kind: "op", Verb: "Run", Label: fmt.Sprintf("command %d", i), Source: uint64(i + 1), Output: 1, Exited: true, Tail: []string{"tail"}, TailOmitted: 1}}})
	}
	catchup := make(chan struct{})
	requests := make(chan sessionapi.NativeRequest, 32)
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		var attachment string
		var original []activity.OutputDemand
		result := func(demand activity.OutputDemand) activity.OutputResult {
			return activity.OutputResult{Demand: demand, View: feed.OutputView{Lines: []string{fmt.Sprintf("retained body %d", demand.Source), "tail"}}}
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-catchup:
				catchup = nil
				frame.ReacquireOutput = true
				// A catch-up window retained only the last of the original outputs.
				frame.MainOutput = []activity.OutputResult{result(original[len(original)-1])}
				_ = api.Native().Publish(attachment, frame)
				frame.MainOutput = nil
			case request := <-api.Native().Requests():
				attachment = request.Attachment
				requests <- request
				if output := request.Operation.Output; output != nil {
					if original == nil {
						original = output.Demands
						request.Reply <- ""
						continue
					}
					for _, demand := range output.Demands {
						frame.MainOutput = append(frame.MainOutput, result(demand))
					}
				}
				if display := request.Operation.Display; display != nil {
					frame.View.Input, frame.View.Displayed = display.Thread, display.Thread
				}
				frame.View.Version.Revision++
				_ = api.Native().Publish(attachment, frame)
				frame.MainOutput = nil
				request.Reply <- ""
			}
		}
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
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture", Resume: "main"})
	connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
	if err != nil {
		t.Fatal(err)
	}
	r := nativeSchedulingRuntime(t, u, connection)
	c := r.active
	c.renderer.terminal.Transcript.Expansion = 2
	painted, err := c.renderer.render(t.Context(), io.Discard, time.Now(), "test")
	if err != nil || len(painted.Demands) < 3 {
		t.Fatalf("renderer hydration demands: %d, %v", len(painted.Demands), err)
	}
	completed := 0
	c.enqueue(sessionapi.NativeOperation{Output: &sessionapi.NativeOutputDemand{Demands: painted.Demands}}, nil, func(err error) {
		if err != nil {
			t.Error(err)
		}
		completed++
	})
	original := c.calls[len(c.calls)-1]
	for c.request(nativeObservationCall) == nil || !c.request(nativeObservationCall).complete {
		nativeSchedulingStep(t, r)
	}
	c.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: c.event.Frame.View.Version, Thread: sessionpresentation.Thread{ID: "side"}}}, nil, nil)
	c.enqueue(sessionapi.NativeOperation{Interrupt: &sessionpresentation.InterruptIntent{Version: c.event.Frame.View.Version}}, nil, nil)
	for c.event.Frame.View.Input.ID != "side" {
		nativeSchedulingStep(t, r)
	}
	if completed != 0 {
		t.Fatal("missing hydration completed before catch-up")
	}
	close(catchup)
	for completed == 0 {
		nativeSchedulingStep(t, r)
	}
	if completed != 1 || c.event.Frame.View.Input.ID != "side" {
		t.Fatal("catch-up duplicated callback or replaced current input")
	}
	var reads, interrupts int
	for len(requests) != 0 {
		request := <-requests
		if request.Operation.Interrupt != nil {
			interrupts++
		}
		if output := request.Operation.Output; output != nil {
			reads++
			if request.Thread != original.target.Thread || request.Attachment != original.target.Attachment || request.Version != original.target.Version {
				t.Fatal("read reacquisition changed the original owner")
			}
			if reads > 1 && (len(output.Demands) != 1 || output.Demands[0] == painted.Demands[len(painted.Demands)-1]) {
				t.Fatal("reacquisition replayed fulfilled output or a multi-output batch")
			}
		}
	}
	if reads != len(original.operation.Output.Demands) || interrupts != 1 {
		t.Fatalf("reads=%d, interrupts=%d", reads, interrupts)
	}
	c.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: c.event.Frame.View.Version, Thread: c.event.Frame.View.Main}}, nil, nil)
	for c.event.Frame.View.Input.ID != "main" {
		nativeSchedulingStep(t, r)
	}
	var output bytes.Buffer
	painted, err = c.renderer.render(t.Context(), &output, time.Now(), "test")
	if err != nil || len(painted.Demands) != 0 {
		t.Fatalf("retained hydration was incomplete: %d, %v", len(painted.Demands), err)
	}
	for i := range 3 {
		if !strings.Contains(output.String(), fmt.Sprintf("retained body %d", i+1)) {
			t.Fatal("original renderer lost hydrated output")
		}
	}
}
