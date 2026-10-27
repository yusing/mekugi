package launch

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

type nativeReattachLauncher struct {
	run     func(context.Context, sessionapi.Start, *sessionapi.Server) error
	prepare func(context.Context, string, sessionapi.InputRequest) (*sessionapi.PreparedInput, error)
}

func (*nativeReattachLauncher) StartOptions(context.Context) (sessionapi.StartOptions, error) {
	return sessionapi.StartOptions{Models: []sessionapi.ModelGroup{{Models: []sessionapi.Model{{ID: "fixture"}}}}}, nil
}

func (l *nativeReattachLauncher) PrepareInput(ctx context.Context, workspace string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
	if l.prepare != nil {
		return l.prepare(ctx, workspace, input)
	}
	return nil, fmt.Errorf("reattachment fixture does not accept initial input")
}

func (l *nativeReattachLauncher) Run(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
	return l.run(ctx, start, api)
}

func TestNativeReattachKeepsSourceUntilDestinationPublication(t *testing.T) {
	for _, test := range []struct{ name, refusal string }{{"saved", ""}, {"save-failed", "fixture layout save failed"}} {
		t.Run(test.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.attached.cwd = t.TempDir()
			u.testMainFrame(80, 24, 0)
			scopes := make(chan context.Context, 4)
			type savedLayout struct {
				thread  string
				update  sessionapi.NativePreferenceUpdate
				request sessionapi.NativeRequest
			}
			saves := make(chan savedLayout, 4)
			actions := make(chan string, 4)
			displayed := make(chan string, 4)
			launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
				thread := start.Resume
				frame := sessionapi.NativeFrame{View: u.attached.presentation.View()}
				frame.View.Main.ID, frame.View.Input.ID, frame.View.Displayed.ID = thread, thread, thread
				if err := api.SetSummary(sessionapi.Summary{Thread: thread, Workspace: u.attached.cwd, Ready: true}); err != nil {
					return err
				}
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case request := <-api.Native().Requests():
						if request.Attach {
							scopes <- request.ViewContext
						}
						_ = api.Native().Publish(request.Attachment, frame)
						if update := request.Operation.Preferences; update != nil {
							saves <- savedLayout{thread, *update, request}
							continue // The test holds acknowledgement independently of navigation.
						}
						if request.Operation.Command != nil {
							actions <- thread
						}
						if request.Operation.Display != nil {
							displayed <- thread
						}
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
			for _, thread := range []string{"main", "destination"} {
				serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture", Resume: thread})
			}
			source, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
			if err != nil {
				t.Fatal(err)
			}
			r := nativeSchedulingRuntime(t, u, source)
			c, sourceScope := r.active, <-scopes
			initial := c.event
			terminal := c.renderer.terminal
			c.editor("main").InsertText("source draft")
			c.preferenceReady = true
			terminal.Split = 55
			c.stagePreferences(false)
			if len(c.calls) == 0 {
				t.Fatal("changed layout was not staged")
			}
			sourceTranscript := terminal.Transcript
			terminal.Transcript.Following = false
			done := make(chan nativeClientReattached, 1)
			c.reattach = "missing"
			c.attachDestination(t.Context(), done)
			failed := <-done
			if c.applyReattachment(failed) != nil || sourceScope.Err() != nil || c.connection != source {
				t.Fatal("failed destination retired source")
			}
			if len(saves) != 0 || terminal.Transcript != sourceTranscript {
				t.Fatal("failed destination changed source layout or navigation")
			}
			terminal.Split = 67
			c.reattach = "destination"
			c.attachDestination(t.Context(), done)
			var ready nativeClientReattached
			select {
			case ready = <-done:
			case <-saves:
				t.Fatal("destination readiness waited for a duplicate attachment-local layout save")
			case <-time.After(5 * time.Second):
				t.Fatal("destination did not publish")
			}
			if ready.err != nil || sourceScope.Err() != nil || ready.event.Frame.View.Main.ID != "destination" {
				t.Fatalf("handoff before destination snapshot: %v", ready.err)
			}
			destinationScope := <-scopes
			if ready.event.Frame.View.Version.Attachment != initial.Frame.View.Version.Attachment || ready.event.Attachment == initial.Attachment {
				t.Fatal("fixture must use distinct streams with equal runtime-local attachment IDs")
			}
			r.reattach(ready, nil)
			destination := r.active
			if destination == c || destination.renderer.terminal.Transcript == sourceTranscript || !destination.renderer.terminal.Transcript.Following {
				t.Fatal("destination inherited source navigation or failed to activate")
			}
			for {
				r.schedule()
				if call := c.request(nativePreferenceCall); call != nil {
					break
				}
				nativeSchedulingStep(t, r)
			}
			var saved savedLayout
			select {
			case saved = <-saves:
			case <-time.After(5 * time.Second):
				t.Fatal("outgoing layout was not dispatched")
			}
			layout, err := terminalsession.ReadPreferences(saved.update.Presentation)
			if err != nil || saved.thread != "main" || layout.Split != 67 || !saved.update.Flush {
				t.Fatalf("final layout did not replace the queued unsent update: %+v, %v", saved, err)
			}
			r.retire()
			if c.closed || sourceScope.Err() != nil || r.active != destination {
				t.Fatal("pending layout retired its source or blocked destination use")
			}
			destination.editor("main").InsertText("destination draft")
			destination.enqueue(sessionapi.NativeOperation{Command: new("/status")}, nil, nil)
			r.schedule()
			select {
			case thread := <-actions:
				if thread != "destination" {
					t.Fatalf("destination control reached %q", thread)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pending layout blocked destination control")
			}
			destination.displayThread(sessionpresentation.Thread{ID: "historical"})
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
		awaitDisplay:
			for {
				r.schedule()
				select {
				case thread := <-displayed:
					if thread != "destination" {
						t.Fatalf("destination display reached %q", thread)
					}
					break awaitDisplay
				case message := <-r.messages:
					if err := r.deliver(message); err != nil {
						t.Fatal(err)
					}
				case call := <-r.results:
					call.complete = true
				case <-deadline.C:
					t.Fatal("pending layout blocked destination display navigation")
				}
				r.settle()
			}
			saved.request.Reply <- test.refusal
			for !c.closed {
				nativeSchedulingStep(t, r)
			}
			if len(saves) != 0 {
				t.Fatal("outgoing layout was sent more than once")
			}
			if feedback := destination.renderer.frame.View.Status.Feedback; test.refusal != "" && (!feedback.Alert || !strings.Contains(feedback.Text, test.refusal)) {
				t.Fatalf("outgoing layout failure was not visible: %+v", feedback)
			}
			if destinationScope.Err() != nil || c.editor("main").Text != "source draft" || destination.editor("main").Text != "destination draft" {
				t.Fatal("destination/source ownership or drafts changed")
			}
		})
	}
}

func TestNativeRestorationCopiesImagesBeforeReceipt(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.testMainFrame(80, 24, 0)
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	c := &nativeClient{ctx: t.Context(), renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}, runtime: &nativeClientRuntime{messages: make(chan nativeClientMessage), editors: make(map[[2]string]*composer.Editor)}}
	source := pasteTestImage(t)
	draft := composer.Draft{Text: "[Image 1]", Images: []composer.Token{{Start: 0, End: 9, Path: source}}}
	effect := sessionapi.NativeEffect{ID: 1, Thread: "main", Restore: &draft}
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Effects: []sessionapi.NativeEffect{effect}}})
	if c.effect != 0 || c.editor("main").Text != "" {
		t.Fatal("image effect acknowledged before acquisition")
	}
	result := *(<-c.runtime.messages).restoration
	// Main's editor survives confirmed replacement while acquisition is in
	// flight. Retained effect metadata follows that explicit switch.
	frame := c.event.Frame
	frame.View.Main.ID, frame.View.Input.ID, frame.View.Displayed.ID = "branch", "branch", "branch"
	frame.View.Version.Attachment++
	frame.Switch = &sessionapi.NativeSwitch{From: "main", To: "branch", Origin: "view"}
	frame.Effects[0].Thread = "branch"
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	c.restorationResult(result)
	c.workers.Wait()
	copied := c.editor("main").Images[0].Path
	defer os.Remove(copied)
	if c.effect != 1 || copied == source || !c.ownedImages[copied] || len(c.editors) != 1 {
		t.Fatal("restoration did not transfer image ownership")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("client deleted server image", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled := copyRestoredEffect(ctx, effect)
	if canceled.err == nil || len(canceled.owned) != 0 {
		t.Fatal("canceled acquisition retained copies")
	}
}

func TestNativeTrackedDeliveriesReleaseClientPacing(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	var effects nativeViewEffects
	u.attached.deliverActivity = func(_ *executionPresentation, deliveries []sessionpresentation.ActivityDelivery) {
		effects.deliver("main", deliveries)
	}
	script := "echo first; echo second"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "first\n"}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1}, segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: "second\n"})
	awaitMain(t, u, "second")
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	publish := func() {
		t.Helper()
		u.testMainFrame(90, 30, 0)
		frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source), Effects: effects.publication(), Activity: effects.activityPublication()}
		data, err := json.Marshal(&frame, jsonv1.FormatDurationAsNano(true))
		if err != nil {
			t.Fatal(err)
		}
		var received sessionapi.NativeFrame
		if err = json.Unmarshal(data, &received, jsonv1.FormatDurationAsNano(true)); err != nil {
			t.Fatal(err)
		}
		c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: received})
	}
	publish()
	if len(c.streams.Commands) != 1 {
		t.Fatal("tracked stream missing")
	}
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
	report.conn.Close()
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "first\nsecond\n"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	serverCLIAwait(t, func() bool {
		flushStreamOutput(&u.attached, u.shell, &u.Client, &u.streams, u.execTrack, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.ctx, u.retained, u.segments, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues)
		return u.attached.session.Commands()[[3]string{"main", "t", "cmd"}] == nil
	})
	publish()
	if len(c.streams.Commands) != 0 {
		t.Fatal("tracked completion left client reveal overlay held")
	}
	c.advancePresentation(time.Now().Add(time.Second))
	rows := terminal.Transcript.RenderFeed(90, 30).Lines
	if !strings.Contains(strings.Join(rows, "\n"), "second") {
		t.Fatalf("final output missing: %q", rows)
	}
}

func TestNativeActivityBurstPreservesAttachment(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	view := n.views["view"]
	u.attached.deliverActivity = n.deliverActivity
	appServerTestTurn(t, u, "turn")
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "burst", "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	c := nativeTestPromptClient(u)
	publish := func() sessionapi.NativeEvent {
		u.testMainFrame(90, 30, 0)
		return sessionapi.NativeEvent{Attachment: "client", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source), Effects: view.effects.publication(), Activity: view.effects.activityPublication()}}
	}
	c.accept(publish())
	view.effects.add(sessionapi.NativeEffect{Thread: "main", Restore: &composer.Draft{Text: "retained draft"}})
	view.effects.add(sessionapi.NativeEffect{Thread: "main", Clipboard: new("retained clipboard")})
	identities := []uint64{view.effects.pending[0].ID, view.effects.pending[1].ID}
	var want strings.Builder
	for i := range 200 {
		delta := fmt.Sprintf("line %d\n", i)
		want.WriteString(delta)
		appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "turn", "itemId": "cmd", "delta": delta})
		// Publications continue while client consumption and receipts are paused.
		publish()
	}
	if view.ctx.Err() != nil || view.effects.reset == 0 || len(view.effects.activity) > 64 || len(view.effects.pending) != 2 {
		t.Fatal("activity publications retired the view or consumed semantic capacity")
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, want.String()
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	frame := publish()
	c.accept(frame)
	if c.editor("main").Text != "retained draft" || c.renderer.terminal.Frame.Clipboard == "" || len(c.streams.Commands) != 0 {
		t.Fatal("catchup lost restoration, clipboard, or command settlement")
	}
	if view.effects.pending[0].ID != identities[0] || view.effects.pending[1].ID != identities[1] {
		t.Fatal("activity compaction changed semantic identities")
	}
	rows := strings.Join(c.renderer.terminal.Transcript.RenderFeed(90, 30).Lines, "\n")
	if !strings.Contains(rows, "line 199") {
		t.Fatalf("catchup left stale output pacing: %q", rows)
	}
	c.renderer.terminal.Frame.Clipboard = ""
	c.accept(frame)
	if c.editor("main").Text != "retained draft" || c.renderer.terminal.Frame.Clipboard != "" {
		t.Fatal("repeated snapshot duplicated a semantic consequence")
	}
	if !view.effects.acknowledge(c.effect, false) || len(view.effects.pending) != 0 || len(view.effects.activity) != 0 {
		t.Fatal("receipt did not settle both delivery classes")
	}
}

func TestNativeAppliedEffectsAcknowledgedBeforePaint(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("paint-queued=%t", queued), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			c.renderer.terminal.Width, c.renderer.terminal.Height = 80, 24
			paint := func() {
				t.Helper()
				painted, err := c.renderer.render(t.Context(), io.Discard, time.Now(), mekugiVersion())
				if err != nil || !painted.Notices.Content {
					t.Fatalf("content paint unavailable: %v %+v", err, painted.Notices)
				}
				c.stageReceipt(&sessionapi.NativeNoticeReceipt{Claim: c.renderer.frame.Notice, Attachment: painted.Notices.Attachment, Feedback: painted.Notices.Feedback, Composer: painted.Notices.Composer, Content: painted.Notices.Content})
			}
			paint()
			if !queued {
				c.calls = nil // The original paint receipt has already been sent.
			}
			u.issues.CleanupNotice("", "", "planning", "new unseen cleanup progress")
			delivery := applyCriticalNotices(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, u.audience, u.issues)
			if delivery == nil || len(u.issues.Notices.Pending()) != 1 {
				t.Fatal("pending progress fixture unavailable")
			}
			effect := sessionapi.NativeEffect{ID: 1, Restore: &composer.Draft{Text: "restored"}}
			c.accept(sessionapi.NativeEvent{Attachment: c.event.Attachment, Frame: sessionapi.NativeFrame{Notice: delivery.id, View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Effects: []sessionapi.NativeEffect{effect}}})
			v := nativeSessionView{notices: delivery}
			v.effects.add(effect)
			deliverReceipt := func() {
				t.Helper()
				for _, call := range c.calls {
					if call.operation.Receipt != nil {
						if refusal := v.applyView(call.operation); refusal != "" {
							t.Fatal(refusal)
						}
					}
				}
				c.calls = nil
			}
			deliverReceipt()
			if c.editor("main").Text != "restored" || len(v.effects.pending) != 0 {
				t.Fatal("effect was not applied and acknowledged before painting")
			}
			if len(u.issues.Notices.Pending()) != 1 {
				t.Fatal("effect acknowledgement consumed unseen progress")
			}
			paint()
			deliverReceipt()
			if len(u.issues.Notices.Pending()) != 0 {
				t.Fatal("newly painted progress was not acknowledged")
			}
		})
	}
}

func TestNativeQueuedPaintReceiptCoalescesEffects(t *testing.T) {
	c := &nativeClient{event: sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1}}}}}
	painted := sessionapi.NativeNoticeReceipt{Attachment: 1, Content: true}
	c.stageReceipt(&painted)
	c.effect = 2
	c.stageReceipt(nil)
	if len(c.calls) != 1 || c.calls[0].operation.Receipt.Effect != 2 || c.calls[0].operation.Receipt.Notice != painted {
		t.Fatal("effect acknowledgement lost a queued paint receipt")
	}
	c.event.Frame.View.Version.Attachment = 2
	c.stageReceipt(nil)
	if len(c.calls) != 1 || c.calls[0].target.Version.Attachment != 2 || c.calls[0].operation.Receipt.Effect != 2 || c.calls[0].operation.Receipt.Notice != (sessionapi.NativeNoticeReceipt{}) {
		t.Fatal("replacement reused another attachment's paint receipt")
	}
}

func TestNativeLocalFailureDetailsClickAndDismiss(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.testMainFrame(80, 24, 0)
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	terminal.Width, terminal.Height = 80, 24
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View()}})
	const message = "The clipboard could not be acquired.\nThe local helper exited with a transport failure."
	c.feedback.Set(message, true, time.Now())
	c.refresh(time.Now())
	paint := func() {
		t.Helper()
		if _, err := c.renderer.render(t.Context(), io.Discard, time.Now(), mekugiVersion()); err != nil {
			t.Fatal(err)
		}
	}
	click := func(rect terminalsession.Rect) {
		t.Helper()
		if rect.W == 0 {
			t.Fatal("notice target was not painted")
		}
		x, y := rect.X+terminal.Layout.Main.X+1, rect.Y+terminal.Layout.Main.Y+1
		c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dM", x, y)})
	}
	paint()
	if terminal.Frame.Notices.Feedback != 0 {
		t.Fatal("local failure acknowledged server feedback")
	}
	click(terminal.Main.NoticeDetails)
	if terminal.Dialog == nil || len(terminal.Dialog.Pages) != 1 || terminal.Dialog.Pages[0].Body != message || len(c.calls) != 0 {
		t.Fatal("local error details were not retained locally")
	}
	paint()
	c.dialog(terminalsession.DialogIntent{Action: terminalsession.DialogClose})
	if terminal.Dialog != nil || c.feedback.Text != message {
		t.Fatal("closing details dismissed its notice")
	}
	paint()
	click(terminal.Main.NoticeDismiss)
	if c.feedback.Text != "" || len(c.calls) != 0 {
		t.Fatal("local dismissal called the server or retained feedback")
	}
}

func TestNativeDetachPreservesUnknownAndUnadmittedDrafts(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.testMainFrame(80, 24, 0)
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View()}})
	submit := func(text string) {
		c.editor("main").InsertText(text)
		_, target := c.selectedEditor()
		c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: c.event.Frame.View.Version, Thread: "main", Target: target, Operation: sessionpresentation.SubmitComposer}})
	}
	submit("unknown")
	call := c.calls[0]
	c.calls = nil
	submit("not dispatched")
	c.runtime = &nativeClientRuntime{active: c, editors: make(map[[2]string]*composer.Editor)}
	c.requests[nativeInputCall] = call
	call.err = context.Canceled
	c.finishPendingCalls()
	if len(c.unknown) != 1 || c.unknown[0].Text != "unknown" || c.editor("main").Text != "not dispatched" || len(c.calls) != 0 {
		t.Fatal("detach lost or replayed submission state")
	}
}

func TestNativeClipboardRejectsOldStream(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard command integration")
	}
	dir := t.TempDir()
	t.Setenv("COMPOSER_TEST_PNG", pasteTestImage(t))
	t.Setenv("WAYLAND_DISPLAY", "test-display")
	if err := os.WriteFile(filepath.Join(dir, "wl-paste"), []byte("#!/bin/sh\n/bin/cat \"$COMPOSER_TEST_PNG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	defer terminal.Diff.Close()
	c := &nativeClient{ctx: t.Context(), renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}, ownedImages: make(map[string]bool), runtime: &nativeClientRuntime{messages: make(chan nativeClientMessage), editors: make(map[[2]string]*composer.Editor)}}
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View()}
	c.accept(sessionapi.NativeEvent{Attachment: "source-view", Frame: frame})
	c.clipboard()
	var result nativeClipboardResult
	select {
	case message := <-c.runtime.messages:
		result = *message.clipboard
	case <-time.After(5 * time.Second):
		t.Fatal("clipboard acquisition did not finish")
	}
	c.workers.Wait()
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer os.Remove(result.path)
	// Independent runtimes can reuse the same numeric presentation version.
	frame.View.Main.ID, frame.View.Displayed.ID, frame.View.Input.ID = "destination", "destination", "destination"
	c.accept(sessionapi.NativeEvent{Attachment: "destination-view", Frame: frame})
	c.clipboardResult(result)
	if len(c.editor("main").Images) != 0 {
		t.Fatal("source clipboard acquisition entered the destination editor")
	}
	if _, err := os.Stat(result.path); !os.IsNotExist(err) {
		t.Fatalf("stale acquired image was not released: %v", err)
	}
}
