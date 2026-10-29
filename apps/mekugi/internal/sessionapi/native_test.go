package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffmodel"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	notice "github.com/yusing/mekugi/modules/notifications/presentation"
)

func nativeTestServer(t *testing.T) (*Native, *http.Client) {
	t.Helper()
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	// The startup-switch fixture confirms this second identity before sending
	// a mutation through the switched view.
	hub.sessions["confirmed"] = &listedSession{entry: listed{ID: "confirmed", Thread: "confirmed", Workspace: "/workspace"}, server: s}
	hub.running[&managedSession{operation: "operation", server: s, inputWorkspace: "/workspace", ctx: t.Context()}] = struct{}{}
	client, _ := serveControl(t, hub.Handler())
	return s.Native(), client
}

func nativeTestFrame(thread string, attachment uint64) NativeFrame {
	return NativeFrame{View: sessionpresentation.View{
		Version: sessionpresentation.Version{Attachment: attachment, Revision: 1},
		Main:    sessionpresentation.Thread{ID: thread, Caller: "/root"},
		Info:    sessionpresentation.MainInfo{Workspace: "/workspace"},
	}}
}

func TestNativeStatusAnchorDoesNotRepublish(t *testing.T) {
	for _, authority := range []viewAuthority{localNativeView, remoteHumanView} {
		native := NewLive().Native()
		source, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", authority)
		if failure != nil {
			t.Fatal(failure)
		}
		defer source.close()
		var model sessionpresentation.Model
		model.Attach(sessionpresentation.Thread{ID: "thread", Caller: "/root"}, false)
		model.Status.Set("Working", false)
		model.Status.Started = time.Unix(100, 0)
		var control sessioncontrol.Live
		control.Thread = "thread"
		control.Started("turn")
		input := sessionpresentation.MainInputs{Info: sessionpresentation.MainInfo{Workspace: "/workspace"}, Control: &control, Conversation: &conversation.Session{}, Feedback: &notice.Model{}}
		publish := func() {
			if err := native.Publish(request.Attachment, NativeFrame{View: model.Publish(input, sessionpresentation.Caption{})}); err != nil {
				t.Fatal(err)
			}
		}
		publish()
		first, ok := source.native.pop()
		var decoded NativeEvent
		if !ok || json.Unmarshal(first.Data, &decoded, nativeEncoding) != nil || !decoded.Frame.View.Status.Session.Started.Equal(model.Status.Started) {
			t.Fatal("status start anchor did not reach the consuming wire type")
		}
		publish()
		if _, ok := source.native.pop(); ok {
			t.Fatal("unchanged status emitted another semantic publication")
		}
		model.Status.Complete("completed", nil, model.Status.Started.Add(time.Minute))
		control.Completed(true)
		publish()
		last, ok := source.native.pop()
		if !ok || json.Unmarshal(last.Data, &decoded, nativeEncoding) != nil || decoded.Frame.View.Status.Session.Completion != time.Minute {
			t.Fatal("settled duration did not publish")
		}
	}
}

func openNativeTest(t *testing.T, n *Native, client *http.Client, frame NativeFrame, startup string) (*NativeConnection, NativeRequest) {
	t.Helper()
	type result struct {
		connection *NativeConnection
		err        error
	}
	done := make(chan result, 1)
	go func() {
		var c *NativeConnection
		var err error
		if startup == "" {
			c, err = OpenNative(t.Context(), client, "http://mekugi", frame.View.Main.ID)
		} else {
			c, err = openNativeSubscription(t.Context(), client, "http://mekugi", "view/attach", RPCParams{Startup: startup})
		}
		done <- result{c, err}
	}()
	request := <-n.Requests()
	if !request.Attach || request.Thread != frame.View.Main.ID || request.Startup != startup {
		t.Fatalf("incorrect attachment admission: %+v", request)
	}
	if err := n.Publish(request.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	request.Reply <- ""
	opened := <-done
	if opened.err != nil {
		t.Fatal(opened.err)
	}
	t.Cleanup(func() { opened.connection.Close() })
	return opened.connection, request
}

func TestNativeSocketIsolationAndEditorPrivacy(t *testing.T) {
	t.Parallel()
	n, client := nativeTestServer(t)
	frame := nativeTestFrame("thread", 1)
	frame.View.Draft.Available = true
	frame.View.Draft.Draft = composer.Draft{Text: "private ordinary draft"}
	frame.View.PromptDraft.Draft = composer.Draft{Text: "private prompt draft"}
	frame.View.Questions.Call = &prompt.CallView{ID: "call", Questions: []prompt.QuestionView{{ID: "secret", Secret: true, Answer: []string{"private secret"}}}}
	frame.Questions = []prompt.CallView{*frame.View.Questions.Call}
	first, firstRequest := openNativeTest(t, n, client, frame, "")
	second, secondRequest := openNativeTest(t, n, client, nativeTestFrame("thread", 2), "")
	event, err := first.Next()
	if err != nil {
		t.Fatal(err)
	}
	if event.Attachment == secondRequest.Attachment || event.Frame.View.Draft.Draft.Text != "" || event.Frame.View.PromptDraft.Draft.Text != "" || len(event.Frame.View.Questions.Call.Questions[0].Answer) != 0 || len(event.Frame.Questions[0].Questions[0].Answer) != 0 {
		t.Fatalf("attachment or editor privacy lost: %+v", event)
	}
	if frame.View.Draft.Draft.Text == "" || len(frame.View.Questions.Call.Questions[0].Answer) != 1 {
		t.Fatal("publication mutated the owner's detached input")
	}
	if _, err := second.Next(); err != nil {
		t.Fatal(err)
	}
	command := "/status"
	done := make(chan error, 1)
	go func() {
		_, err := first.Send(t.Context(), event.Target(), NativeOperation{Command: &command})
		done <- err
	}()
	mutation := <-n.Requests()
	if mutation.Attach || mutation.Attachment != firstRequest.Attachment || mutation.Thread != "thread" || mutation.Workspace != "/workspace" || *mutation.Operation.Command != command {
		t.Fatalf("mutation lost captured identity: %+v", mutation)
	}
	mutation.Reply <- ""
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-mutation.Context.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("completed RPC request retained its request lifetime")
	}
	if mutation.ViewContext.Err() != nil {
		t.Fatal("completing a mutation detached the view")
	}
	first.Close()
	select {
	case <-firstRequest.ViewContext.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not release the presentation attachment")
	}
	if secondRequest.ViewContext.Err() != nil {
		t.Fatal("disconnect canceled another presentation attachment")
	}
}

func TestNativeStaleAndCanceledMutations(t *testing.T) {
	t.Parallel()
	n, client := nativeTestServer(t)
	frame := nativeTestFrame("thread", 1)
	c, attach := openNativeTest(t, n, client, frame, "")
	event, err := c.Next()
	if err != nil {
		t.Fatal(err)
	}
	frame.View.Version.Attachment++
	if err := n.Publish(attach.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	command := "/status"
	_, err = c.Send(t.Context(), event.Target(), NativeOperation{Command: &command})
	var refusal *NativeFailure
	if !errors.As(err, &refusal) || refusal.Status != http.StatusConflict {
		t.Fatalf("stale view was not refused: %v", err)
	}
	event, err = c.Next()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := c.Send(ctx, event.Target(), NativeOperation{Command: &command}); done <- err }()
	request := <-n.Requests()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled mutation reported success")
	}
	select {
	case <-request.Context.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("RPC cancellation did not reach admission")
	}
	if request.ViewContext.Err() != nil {
		t.Fatal("mutation cancellation retired its presentation scope")
	}
	request.Reply <- ""
}

func TestNativeMenuReachesOwnerAfterPublicationProgress(t *testing.T) {
	t.Parallel()
	n, client := nativeTestServer(t)
	frame := nativeTestFrame("thread", 1)
	c, attach := openNativeTest(t, n, client, frame, "")
	event, err := c.Next()
	if err != nil {
		t.Fatal(err)
	}
	frame.View.Version.Revision++
	if err := n.Publish(attach.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	action := sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: 7, Kind: sessionpresentation.MenuDismiss}
	done := make(chan error, 1)
	go func() { _, err := c.Send(t.Context(), event.Target(), NativeOperation{Menu: &action}); done <- err }()
	select {
	case request := <-n.Requests():
		if request.Version != event.Frame.View.Version || request.Operation.Menu.Version != action.Version || request.Operation.Menu.Menu != 7 {
			t.Fatal("transport silently retargeted the menu")
		}
		request.Reply <- "the captured menu closed"
	case err := <-done:
		t.Fatalf("transport rejected menu before owner recheck: %v", err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "the captured menu closed") {
		t.Fatalf("owner menu refusal was lost: %v", err)
	}
}

func TestNativeStartupExplicitLogicalSwitch(t *testing.T) {
	t.Parallel()
	n, client := nativeTestServer(t)
	frame := nativeTestFrame("", 1)
	c, request := openNativeTest(t, n, client, frame, "operation")
	event, err := c.Next()
	if err != nil || event.Startup != "operation" || event.Frame.View.Main.ID != "" {
		t.Fatalf("startup fabricated a thread: %+v, %v", event, err)
	}
	frame.View.Main.ID = "confirmed"
	if err := n.Publish(request.Attachment, frame); err == nil {
		t.Fatal("silent startup identity replacement was admitted")
	}
	frame.Switch = &NativeSwitch{To: "confirmed", Origin: request.Attachment}
	if err := n.Publish(request.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	old := event.Target()
	event, err = c.Next()
	if err != nil {
		t.Fatal(err)
	}
	command := "/status"
	if _, err := c.Send(t.Context(), old, NativeOperation{Command: &command}); err == nil {
		t.Fatal("startup input silently followed a replacement publication")
	}
	done := make(chan error, 1)
	go func() { _, err := c.Send(t.Context(), event.Target(), NativeOperation{Command: &command}); done <- err }()
	mutation := <-n.Requests()
	if mutation.Startup != "" || mutation.Thread != "confirmed" {
		t.Fatalf("mutation retained the startup target: %+v", mutation)
	}
	mutation.Reply <- ""
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNativeOrderedDeliveryCapacityClosesOnlyView(t *testing.T) {
	t.Parallel()
	n := NewLive().Native()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	n.views["slow"] = &nativeView{target: requestTarget{"thread", "/workspace"}, ctx: ctx, cancel: cancel, queue: &nativePublications{ready: make(chan struct{}, 1)}}
	frame := nativeTestFrame("thread", 1)
	// Output from distinct presentation attachments cannot be moved into the
	// latest frame. This is irreducible delivery, rather than progress backlog.
	frame.MainOutput = []activity.OutputResult{{}}
	if err := n.Publish("slow", frame); err != nil {
		t.Fatal(err)
	}
	for range subscriberBuffer - 1 {
		frame.View.Version.Attachment++
		if err := n.Publish("slow", frame); err != nil {
			t.Fatal(err)
		}
	}
	frame.View.Version.Attachment++
	if err := n.Publish("slow", frame); err == nil || ctx.Err() == nil {
		t.Fatal("overflow retained a blocked viewer")
	}
}

func TestNativePublicationSuppressesOnlyIdenticalFrames(t *testing.T) {
	t.Parallel()
	n := NewLive().Native()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	v := &nativeView{target: requestTarget{"thread", "/workspace"}, ctx: ctx, cancel: cancel, queue: &nativePublications{ready: make(chan struct{}, 1)}}
	n.views["idle"] = v
	frame := nativeTestFrame("thread", 1)
	for range 3 {
		if err := n.Publish("idle", frame); err != nil {
			t.Fatal("unchanged publication filled the stream:", err)
		}
	}
	if data, _ := v.queue.pop(); data.Kind != "snapshot" {
		t.Fatalf("initial publication: %+v", data)
	}
	// Facts can change without a presentation revision, so revision-only
	// suppression would lose an actual update.
	frame.Facts.ActionRequired = true
	if err := n.Publish("idle", frame); err != nil {
		t.Fatal(err)
	}
	if data, _ := v.queue.pop(); data.Kind != "patch" {
		t.Fatalf("facts update: %+v", data)
	}
	frame.View.Version.Attachment++
	if err := n.Publish("idle", frame); err != nil {
		t.Fatal(err)
	}
	if data, _ := v.queue.pop(); data.Kind != "snapshot" {
		t.Fatalf("replacement publication: %+v", data)
	}
	// Feed identity is independent of the session view revision. New immutable
	// publications and in-place changes to non-feed caller values both publish.
	frame.Main = &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Text: "new history"}}}}
	frame.View.Info.Skills = []string{"first"}
	for _, mutate := range []func(){func() {}, func() { frame.View.Info.Skills[0] = "second" }} {
		mutate()
		if err := n.Publish("idle", frame); err != nil {
			t.Fatal(err)
		}
		if data, _ := v.queue.pop(); data.Kind != "patch" {
			t.Fatal("lost a feed or non-feed update without a view revision")
		}
	}
	frame.Review = &changes.ReviewView{Revision: 1, Coverage: "loading"}
	for _, mutate := range []func(){
		func() {},
		func() { frame.Review.Coverage = "complete" },
		func() {
			frame.Review = &changes.ReviewView{Revision: 2, Coverage: "complete", Publication: changes.Publication{
				Files: []changes.File{{Path: "changed.go"}},
			}}
		},
		func() { frame.Review = nil },
	} {
		mutate()
		if err := n.Publish("idle", frame); err != nil {
			t.Fatal(err)
		}
		if data, _ := v.queue.pop(); data.Kind != "patch" {
			t.Fatal("lost saved-review replacement or coverage update")
		}
	}
}

func TestNativePublicationMetadataCostDoesNotScaleWithOwners(t *testing.T) {
	allocatedBytes := func(entries int, authority viewAuthority, changing bool) uint64 {
		n := NewLive().Native()
		source, request, failure := n.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", authority)
		if failure != nil {
			t.Fatal(failure)
		}
		defer source.close()
		frame := nativeTestFrame("thread", 1)
		frame.Main = &activity.Publication{Entries: make([]activity.Entry, entries)}
		for i := range frame.Main.Entries {
			frame.Main.Entries[i].Text = strings.Repeat("history ", 32)
		}
		frame.Family = &Family{Root: "thread", Agents: &activity.Publication{MainSummary: make([]activity.Entry, entries)}}
		for i := range frame.Family.Agents.MainSummary {
			frame.Family.Agents.MainSummary[i].Text = strings.Repeat("family summary ", 32)
		}
		frame.Review = &changes.ReviewView{Revision: 1, Publication: changes.Publication{
			Files: []changes.File{{Path: "large.go", Chunks: []changes.Chunk{{Review: diffmodel.ReviewFile{
				Diff: strings.Repeat("+saved diff\n", entries*1024),
			}}}}},
		}}
		if err := n.Publish(request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
		source.native.pop()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range 5 {
			if changing {
				frame.View.Version.Revision++
			}
			// The owner returns a new wrapper over the same immutable content.
			frame.Review = new(*frame.Review)
			if err := n.Publish(request.Attachment, frame); err != nil {
				t.Fatal(err)
			}
			source.native.pop()
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / 5
	}
	for _, authority := range []viewAuthority{localNativeView, remoteHumanView} {
		for _, changing := range []bool{false, true} {
			small, large := allocatedBytes(1, authority, changing), allocatedBytes(1000, authority, changing)
			if large > small+128<<10 {
				t.Fatalf("publication bytes scale with history for authority %d (changing=%v): small=%d large=%d", authority, changing, small, large)
			}
		}
	}
}

func TestNativeFamilyQuestionRoutingKeepsAuthorityAndWorkspace(t *testing.T) {
	for _, authority := range []viewAuthority{localNativeView, remoteHumanView} {
		t.Run(fmt.Sprint(authority), func(t *testing.T) {
			root, child := NewLive(), NewPreparedLive(func(_ context.Context, workspace string, input InputRequest) (*PreparedInput, error) {
				if workspace != "/child" {
					return nil, fmt.Errorf("prepared in %q", workspace)
				}
				return NewPreparedInput(input.NativeDraft.Clone(), nil), nil
			})
			child.frame.Store(&Frame{Session: Session{Thread: "child", Workspace: "/child"}})
			root.questionOwner = func(call string) *Server {
				if call == "child-call" {
					return child
				}
				return nil
			}
			owner := child.Native()
			received := make(chan NativeRequest, 1)
			go func() {
				for {
					select {
					case <-t.Context().Done():
						return
					case request := <-owner.Requests():
						if request.Attach {
							frame := nativeTestFrame("child", 9)
							frame.View.Info.Workspace = "/child"
							if err := owner.Publish(request.Attachment, frame); err != nil {
								request.Reply <- err.Error()
								continue
							}
						} else {
							received <- request
						}
						request.Reply <- ""
					}
				}
			}()
			n := root.Native()
			sub, attach, failure := n.newView(t.Context(), requestTarget{"root", "/workspace"}, "", authority)
			if failure != nil {
				t.Fatal(failure)
			}
			defer sub.close()
			frame := nativeTestFrame("root", 1)
			frame.Questions = []prompt.CallView{{ID: "root-call", Thread: "root"}}
			frame.Family = &Family{Root: "root", Questions: map[string][]prompt.CallView{"root": {{ID: "stale-root", Thread: "root"}}, "child": {{ID: "child-call", Thread: "child", Questions: []prompt.QuestionView{{ID: "child-call/0", Pending: true, Secret: true, Text: "private"}}}}}}
			if err := n.Publish(attach.Attachment, frame); err != nil {
				t.Fatal(err)
			}
			publication, _ := sub.native.pop()
			event, err := decodeNativeEvent(publication.Kind, publication.Data, NativeEvent{})
			if err != nil {
				t.Fatal(err)
			}
			if len(event.Frame.Questions) != 2 || event.Frame.Questions[0].ID != "root-call" || (authority == remoteHumanView && event.Frame.Questions[1].Questions[0].Text != "") {
				t.Fatalf("shared/redacted inventory: %+v", event.Frame.Questions)
			}
			operation := NativeOperation{Answers: &NativeAnswers{Call: "child-call", Answers: []NativeQuestionAnswer{{AnswerInput: prompt.AnswerInput{Call: "child-call", Question: "child-call/0", Draft: composer.Draft{Text: "answer"}}}}}}
			var body []byte
			if authority == remoteHumanView {
				body, err = json.Marshal(RemoteMutation{Attachment: event.Attachment, Version: event.Frame.View.Version, Operation: RemoteOperation{Answers: &Answer{Call: "child-call", Answers: []QuestionAnswer{{Question: "child-call/0", Text: "answer"}}}}}, nativeEncoding)
			} else {
				body, err = json.Marshal(NativeMutation{Attachment: event.Attachment, Version: event.Frame.View.Version, Operation: operation}, nativeEncoding)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, failure := n.request(t.Context(), requestTarget{"root", "/workspace"}, "", authority, body, make(chan struct{}, 1))
			if failure != nil {
				t.Fatal(failure)
			}
			request := <-received
			if request.Thread != "child" || request.Workspace != "/child" || request.RemoteHuman() != (authority == remoteHumanView) || result.Version.Attachment != 1 {
				t.Fatalf("routed response lost origin or destination: %+v %+v", request, result)
			}
			if authority == remoteHumanView && request.Operation.Answers.PublicAnswer() == nil {
				t.Fatal("public answer gained native authority")
			}
		})
	}
}
