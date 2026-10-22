package launch

import (
	"context"
	"encoding/base64"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// The harness joins real public transport to the same owner adapters used on the
// execution loop. Tests call adapters synchronously so assertions own the stores.
type remoteViewHarness struct {
	t      *testing.T
	u      *appServerUI
	api    *sessionapi.Server
	views  *nativeViews
	view   *nativeSessionView
	server *httptest.Server
	rpc    *sessionapi.RPCClient
	event  sessionapi.NativeEvent
}

func remoteOwnerView(t *testing.T, u *appServerUI) *remoteViewHarness {
	t.Helper()
	h := &remoteViewHarness{t: t, u: u}
	launcher := &nativeReattachLauncher{
		prepare: prepareAPIInput,
		run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
			h.api = api
			h.views = newNativeViews(ctx, api.Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
			defer h.views.close()
			if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: u.attached.cwd, Ready: true}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	hub := sessionapi.NewHub(t.Context(), launcher)
	t.Cleanup(func() { hub.Close() })
	h.server = httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	t.Cleanup(h.server.Close)
	var err error
	h.rpc, err = sessionapi.OpenRPC(t.Context(), h.server.Client(), h.server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.rpc.Close() })
	serverRPCCall(t, h.rpc, "session/start", "", sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture", Resume: u.Thread})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	var subscription *sessionapi.RPCSubscription
	go func() {
		var err error
		subscription, err = h.rpc.Subscribe(ctx, "view/attach", sessionapi.RPCParams{Thread: u.Thread})
		opened <- err
	}()
	var request sessionapi.NativeRequest
	select {
	case request = <-h.api.Native().Requests():
	case err := <-opened:
		t.Fatalf("view attach ended before owner admission: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !request.RemoteHuman() {
		t.Fatal("route lost remote authority")
	}
	if refusal := h.views.attach(request, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	h.view = h.views.views[request.Attachment]
	h.event.Attachment = request.Attachment
	h.publish()
	request.Reply <- ""
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	event, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(event.Data, &h.event, jsonv1.FormatDurationAsNano(true)); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *remoteViewHarness) publish() {
	u, v := h.u, h.view
	view := publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, u.side, time.Now(), true)
	frame := sessionapi.NativeFrame{View: view, Questions: nativePendingQuestions(u.attached.conversation), Main: v.attached.source.Publication(v.attached.presentation, nil)}
	v.publishDetails(&frame)
	if err := h.api.Native().Publish(h.event.Attachment, frame); err != nil {
		h.t.Fatal(err)
	}
	h.event.Frame = frame
}

func (h *remoteViewHarness) send(operation any, apply func(sessionapi.NativeRequest) string) (int, string) {
	h.t.Helper()
	data, err := json.Marshal(map[string]any{"attachment": h.event.Attachment, "version": h.event.Frame.View.Version, "operation": operation}, jsonv1.FormatDurationAsNano(true))
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.rpc.Call(ctx, "view/intent", sessionapi.RPCParams{Thread: h.u.Thread, Body: data}, nil)
	}()
	select {
	case request := <-h.api.Native().Requests():
		_, refusal := h.views.admit(request, &h.u.attached, h.u.Thread)
		if refusal == "" {
			refusal = apply(request)
		}
		request.Reply <- refusal
		err = <-done
	case err = <-done:
	case <-ctx.Done():
		h.t.Fatal(ctx.Err())
	}
	if err == nil {
		return http.StatusAccepted, ""
	}
	if failure, ok := errors.AsType[*sessionapi.RPCError](err); ok && failure.Data != nil {
		return failure.Data.Status, failure.Message
	}
	h.t.Fatal("intent transport failed:", err)
	return 0, ""
}

func TestRemoteViewAnswersUsePendingPromptOwner(t *testing.T) {
	for _, tc := range []struct {
		name         string
		choice       *int
		text         string
		skip, secret bool
		want         string
	}{
		{name: "absent choice", text: "written answer", want: "written answer"},
		{name: "explicit written choice", choice: new(2), text: "explicit answer", want: "explicit answer"},
		{name: "offered choice", choice: new(0), text: "ignored", want: "Customers"},
		{name: "skip", skip: true},
		{name: "secret text", secret: true, text: "remote secret"},
		{name: "secret empty", secret: true},
		{name: "secret skip", secret: true, skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			u.Turn = "turn"
			if tc.secret {
				questionTestSync(t, u, "secret", true)
			} else {
				questionTestAsync(t, u, "ordinary", "Audience?")
			}
			call := nativePendingQuestions(u.attached.conversation)[0]
			h := remoteOwnerView(t, u)
			answer := sessionapi.Answer{Call: call.ID, Answers: []sessionapi.QuestionAnswer{{Question: call.Questions[0].ID, Choice: tc.choice, Text: tc.text, Skip: tc.skip}}}
			code, body := h.send(map[string]any{"answers": answer}, func(request sessionapi.NativeRequest) string {
				refusal, err := applyNativeAnswers(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, u.side, u.clock, &u.questions, &u.approvals, false, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, u.ctx, u.controlObservations, u.review, &u.pendingEditReceipts, *request.Operation.Answers, request.Prepared, request.Context, request.RemoteHuman())
				if err != nil {
					t.Fatal(err)
				}
				return refusal
			})
			if tc.secret {
				if code != 409 || !strings.Contains(body, "terminal") || wire.Len() != 0 || u.questions.Calls[0].Sent {
					t.Fatal("remote secret target reached response authority", code, body)
				}
				return
			}
			if code != 202 {
				t.Fatal(code, body)
			}
			if tc.skip {
				if wire.Len() != 0 || !u.questions.Calls[0].Resolved {
					t.Fatal("all-skipped call did not resolve without dispatch")
				}
				return
			}
			requests := appServerTurnRequests(t, wire)
			if len(requests) != 1 || !strings.Contains(requests[0].text(), tc.want) {
				t.Fatal("remote answer did not use ordinary host submission")
			}
		})
	}
}

func TestRemoteViewCommandAndDetailUseSharedOwners(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	seq := u.attached.source.LastSeq + 1
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: seq, Agent: "Main", Kind: "error", Text: "failure detail"}}})
	h := remoteOwnerView(t, u)
	code, body := h.send(sessionapi.NativeOperation{Command: new("/status")}, func(request sessionapi.NativeRequest) string {
		v := h.view
		u.attached, u.shell, u.picker = v.attached, v.shell, v.picker
		u.Editor = &composer.Editor{Text: *request.Operation.Command}
		_, err := u.applyComposerInput(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: request.Version, Thread: u.Thread, Operation: sessionpresentation.SubmitComposer}})
		if err != nil {
			return err.Error()
		}
		return ""
	})
	if code != 202 || u.statusPanel == nil || wire.Len() == 0 {
		t.Fatal("remote status did not reach existing owner", code, body)
	}
	request, ok := u.Requests.Lookup("1")
	if !ok || request.Owner.view != h.event.Attachment {
		t.Fatal("remote status lost origin attribution")
	}
	h.publish()
	code, body = h.send(sessionapi.NativeOperation{Details: &sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq}}, func(request sessionapi.NativeRequest) string { return h.view.applyView(request.Operation) })
	if code != 202 {
		t.Fatal(code, body)
	}
	frame := nativeDetailFrame(h.view)
	if frame.Details == nil || len(frame.Details.View.Pages) != 1 || frame.Details.View.Pages[0].Body != "failure detail" {
		t.Fatal("remote detail did not borrow execution source")
	}
	captured := h.event
	h.view.attached.feedback.Set("progress before remote detail admission", false, time.Now())
	h.publish()
	if h.event.Frame.View.Version == captured.Frame.View.Version {
		t.Fatal("fixture did not advance publication")
	}
	h.event = captured
	details := &sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq}
	code, body = h.send(sessionapi.NativeOperation{Details: details}, func(request sessionapi.NativeRequest) string { return h.view.applyView(request.Operation) })
	if code != 202 {
		t.Fatal("progress invalidated the unchanged remote destination", code, body)
	}
	details.Displayed = captured.Frame.View.Displayed
	code, body = h.send(sessionapi.NativeOperation{Details: details}, func(request sessionapi.NativeRequest) string { return h.view.applyView(request.Operation) })
	if code != 202 {
		t.Fatal("identified remote detail rejected progress", code, body)
	}
}

func TestRemoteViewDescriptorsUsePublicSnapshotAndLimits(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	if err := os.WriteFile(filepath.Join(u.attached.cwd, "source.txt"), []byte("host snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	h := remoteOwnerView(t, u)
	input := &sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "path", Path: "source.txt"}, {Source: "upload", Name: "label.txt", Data: new(base64.StdEncoding.EncodeToString([]byte("uploaded snapshot")))}}}
	operation := sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}, Input: input}}
	code, body := h.send(operation, func(request sessionapi.NativeRequest) string {
		draft := request.Prepared.Draft
		var frames []string
		for _, envelope := range draft.Attachments {
			decoded, ok := attachments.Decode(envelope)
			if !ok {
				t.Fatal("invalid attachment envelope")
			}
			frames = append(frames, decoded...)
		}
		if len(frames) != 2 || !strings.Contains(frames[0], "host snapshot") || !strings.Contains(frames[1], "uploaded snapshot") {
			t.Fatal("public descriptor order or content changed")
		}
		if !request.Prepared.Admit(request.Context, u.ownedImages) {
			t.Fatal("prepared input could not transfer to existing owner")
		}
		return ""
	})
	if code != 202 {
		t.Fatal(code, body)
	}
	// A structurally valid upload that exceeds the aggregate budget is refused
	// by the same preparer before the execution loop sees it.
	chunk := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", sessionapi.UploadLimit/2)))
	input.Attachments = []sessionapi.Attachment{{Source: "upload", Name: "a", Data: &chunk}, {Source: "upload", Name: "b", Data: &chunk}, {Source: "upload", Name: "c", Data: new("eA==")}}
	code, body = h.send(operation, func(sessionapi.NativeRequest) string { t.Fatal("over-limit input reached owner"); return "" })
	if code != 413 {
		t.Fatalf("combined upload limit: %d %s", code, body)
	}
}

func TestRemoteOutputDemandRejectsNegativeHandle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	seq := u.attached.source.LastSeq + 1
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: seq, Agent: "Main", Kind: "tool", Text: "safe", Native: &sessionpresentation.NativeItem{}}}})
	h := remoteOwnerView(t, u)
	code, body := h.send(map[string]any{"output": map[string]any{"demands": []any{map[string]any{"Source": seq, "Output": -1}}}}, func(request sessionapi.NativeRequest) string {
		return h.view.applyView(request.Operation)
	})
	if code != http.StatusAccepted || len(h.view.mainOutput) != 1 || !h.view.mainOutput[0].View.Released {
		t.Fatalf("unavailable remote output: %d %s %+v", code, body, h.view.mainOutput)
	}
}
