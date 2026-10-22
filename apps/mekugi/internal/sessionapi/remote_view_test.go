package sessionapi

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func openRemoteTest(t *testing.T, s *Server, h http.Handler, frame NativeFrame) (NativeEvent, NativeRequest, *RPCSubscription) {
	t.Helper()
	client := openTestRPC(t, h, false)
	type attached struct {
		sub *RPCSubscription
		err error
	}
	done := make(chan attached, 1)
	go func() {
		sub, err := client.Subscribe(t.Context(), "view/attach", RPCParams{Thread: frame.View.Main.ID})
		done <- attached{sub, err}
	}()
	request := <-s.Native().Requests()
	if !request.RemoteHuman() || !request.Attach {
		t.Fatal("public route did not establish remote view authority")
	}
	if err := s.Native().Publish(request.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	request.Reply <- ""
	opened := <-done
	if opened.err != nil {
		t.Fatal(opened.err)
	}
	notification, err := opened.sub.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var event NativeEvent
	if err := json.Unmarshal(notification.Data, &event, nativeEncoding); err != nil {
		t.Fatal(err)
	}
	return event, request, opened.sub
}

func remotePost(t *testing.T, handler http.Handler, native bool, event NativeEvent, operation any) <-chan testReply {
	t.Helper()
	body := testJSON(t, struct {
		Attachment string                      `json:"attachment"`
		Version    sessionpresentation.Version `json:"version"`
		Operation  any                         `json:"operation"`
		Authority  string                      `json:"authority"`
	}{event.Attachment, event.Frame.View.Version, operation, "local-native"})
	client := openTestRPC(t, handler, native)
	done := make(chan testReply, 1)
	go func() { done <- client.call(t, "view/intent", RPCParams{Thread: event.Frame.View.Main.ID, Body: body}) }()
	return done
}

func TestRemoteViewAuthorityAndOperationBoundary(t *testing.T) {
	t.Parallel()
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	handler := hub.Handler()
	event, _, _ := openRemoteTest(t, s, handler, nativeTestFrame("thread", 1))
	done := remotePost(t, handler, false, event, NativeOperation{Command: new("/status")})
	request := <-s.Native().Requests()
	if !request.RemoteHuman() || request.Operation.Command == nil || *request.Operation.Command != "/status" {
		t.Fatal("JSON changed route authority or semantic command")
	}
	request.Reply <- ""
	if got := (<-done).Code; got != 202 {
		t.Fatal(got)
	}
	if got := (<-remotePost(t, handler, true, event, NativeOperation{Command: new("/status")})).Code; got != 409 {
		t.Fatal("native route accepted remote attachment", got)
	}
	// The inverse crossing is tested against an actual native attachment.
	server := httptest.NewServer(Versioned(handler))
	defer server.Close()
	opened := make(chan *NativeConnection, 1)
	go func() {
		c, err := OpenNative(t.Context(), server.Client(), server.URL, "thread")
		if err != nil {
			t.Error(err)
		}
		opened <- c
	}()
	attach := <-s.Native().Requests()
	if attach.RemoteHuman() {
		t.Fatal("native route lost local authority")
	}
	if err := s.Native().Publish(attach.Attachment, nativeTestFrame("thread", 2)); err != nil {
		t.Fatal(err)
	}
	attach.Reply <- ""
	connection := <-opened
	defer connection.Close()
	nativeEvent, err := connection.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got := (<-remotePost(t, handler, false, nativeEvent, NativeOperation{Command: new("/status")})).Code; got != 409 {
		t.Fatal("remote route accepted native attachment", got)
	}
	for _, operation := range []NativeOperation{
		{SaveDraft: new("private")}, {Preferences: &NativePreferenceUpdate{}},
		{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.AttachClipboard}}},
		{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.EditExternally}}},
		{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.AcceptPrediction}}},
		{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}, Draft: composer.Draft{Text: "raw"}}},
		{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.QueueComposer}}},
		{Command: new("/status"), Editor: &NativeEditorInput{Draft: composer.Draft{Attachments: []string{"prepared"}}}},
		{Completion: &NativeEditorInput{Draft: composer.Draft{Files: []composer.Token{{Path: "/private"}}}}},
	} {
		if got := (<-remotePost(t, handler, false, event, operation)).Code; got != 400 {
			t.Fatal("native-only input reached owner", got)
		}
	}
	for _, forbidden := range []string{"Draft", "Secret", "Incomplete"} {
		answer := map[string]any{"call": "call", "answers": []any{map[string]any{"question": "q"}}}
		if forbidden == "Incomplete" {
			answer[forbidden] = false
		} else {
			answer["answers"].([]any)[0].(map[string]any)[forbidden] = ""
		}
		if got := (<-remotePost(t, handler, false, event, map[string]any{"answers": answer})).Code; got != 400 {
			t.Fatal("native answer metadata accepted", forbidden, got)
		}
	}
	if _, err := connection.Send(t.Context(), nativeEvent.Target(), NativeOperation{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}, Input: &InputRequest{Text: "mixed"}}}); err == nil {
		t.Fatal("native composer accepted remote descriptor form")
	}
}

func TestRemoteViewDetachedSecretProjection(t *testing.T) {
	t.Parallel()
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "view", s, Summary{Thread: "view", Workspace: "/workspace"})
	frame := nativeTestFrame("view", 1)
	q := prompt.QuestionView{ID: "secret", Secret: true, Pending: true, Header: "hidden-header", Text: "hidden-description", Choices: []prompt.Option{{Label: "hidden-option"}}, Answer: []string{"hidden-answer"}}
	frame.Questions = []prompt.CallView{{ID: "call", Questions: []prompt.QuestionView{q, {ID: "ordinary", Text: "ordinary-question"}}}}
	frame.View.Questions.Call = &frame.Questions[0]
	value := activity.Value{Content: activityfeed.Content{Body: "hidden-body"}, Questions: []activity.Question{{Secret: true, Text: "hidden-description", Answer: "hidden-answer", Options: "hidden-option"}, {Text: "ordinary-question", Answer: "ordinary-answer"}}}
	entry := activity.Entry{Facts: activity.Facts{Text: "2 questions\nhidden-description"}, Values: []activity.Value{value, {Content: activityfeed.Content{Body: "hidden-nested-body"}, Members: []activity.Value{value}, RequestedOperations: []activity.Value{value}}}}
	frame.Main = &activity.Publication{Entries: []activity.Entry{entry}, MainSummary: []activity.Entry{entry}, Roster: []activity.AgentRow{{Agent: activity.Agent{Summary: []activity.Entry{entry}}}}}
	frame.Agents = frame.Main
	frame.Details = &NativeDetails{View: sessionpresentation.DetailView{Pages: []sessionpresentation.DetailItem{{DetailPage: activity.DetailPage{Value: value, ID: "page"}}}}}
	frame.Facts = NativeSessionFacts{History: []composer.HistoryEntry{{Draft: composer.Draft{Text: "hidden-local-history"}}}, DebugDirectory: "hidden-debug", Diagnostics: "hidden-host-stderr", Preferences: []byte(`{"private":"hidden-preferences"}`)}
	event, _, client := openRemoteTest(t, s, hub.Handler(), frame)
	data, err := json.Marshal(event, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("hidden-")) || !bytes.Contains(data, []byte("ordinary-question")) || !bytes.Contains(data, []byte("ordinary-answer")) {
		t.Fatal("remote projection leaked secret/native content or lost ordinary content")
	}
	if frame.Questions[0].Questions[0].Text != "hidden-description" || frame.Main.Entries[0].Values[0].Body != "hidden-body" || frame.Details.View.Pages[0].Questions[0].Answer != "hidden-answer" {
		t.Fatal("remote projection mutated native/owner publication")
	}
	s.Native().Finish(&NativeExit{Code: 1, Message: "hidden-runtime-diagnostic"})
	exit, err := client.Next(t.Context())
	if err != nil || exit.Kind != "exit" {
		t.Fatalf("missing joined remote exit: %+v, %v", exit, err)
	}
	if bytes.Contains(exit.Data, []byte("hidden-")) {
		t.Fatal("remote exit exposed private diagnostic")
	}
	if _, err := client.Next(t.Context()); err == nil {
		t.Fatal("joined exit did not close subscription")
	}
}

func TestRemoteViewPublicPreparationAndRefusalCleanup(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "acquired-image")
	if err := os.WriteFile(path, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewPreparedLive(func(ctx context.Context, cwd string, input InputRequest) (*PreparedInput, error) {
		if cwd != "/workspace" || input.NativeDraft != nil || len(input.Attachments) != 2 || input.Attachments[0].Source != "upload" || input.Attachments[1].Path != "relative.txt" {
			t.Error("remote composer bypassed public descriptor preparation")
		}
		return NewPreparedInput(composer.Draft{Text: "snapshot"}, []string{path}), nil
	})
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	event, _, _ := openRemoteTest(t, s, hub.Handler(), nativeTestFrame("thread", 1))
	done := remotePost(t, hub.Handler(), false, event, NativeOperation{Composer: &NativeComposerInput{
		Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer},
		Input:  &InputRequest{Attachments: []Attachment{{Source: "upload", Name: "label", Data: new("Ynl0ZXM=")}, {Source: "path", Path: "relative.txt"}}},
	}})
	request := <-s.Native().Requests()
	if request.Prepared == nil || request.Prepared.Draft.Text != "snapshot" || !request.RemoteHuman() {
		t.Fatal("public preparation did not reach shared owner")
	}
	request.Reply <- "Current admission refused."
	if got := (<-done).Code; got != 409 {
		t.Fatal(got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("refused remote acquired image retained", err)
	}
}

func TestRemoteViewRequiresConfirmedThread(t *testing.T) {
	t.Parallel()
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "startup", s, Summary{Workspace: "/workspace"})
	client := openTestRPC(t, hub.Handler(), false)
	reply := client.call(t, "view/attach", RPCParams{Thread: "startup", Subscription: "view"})
	if reply.Code != http.StatusConflict {
		t.Fatal("unconfirmed public view admitted", reply.Code)
	}
	select {
	case <-s.Native().Requests():
		t.Fatal("unconfirmed view reached owner")
	default:
	}
}

func TestGeneratedRemoteAnswerUsesPublicShape(t *testing.T) {
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	defer hub.Close()
	handler := hub.Handler()
	event, _, _ := openRemoteTest(t, s, handler, nativeTestFrame("thread", 1))
	// The same Go DTO generates RemoteOperation in protocol.ts and supplies the
	// real remote decoder, so native Answer metadata cannot leak into this shape.
	operation := RemoteOperation{Answers: &Answer{Call: "call", Answers: []QuestionAnswer{{Question: "question", Text: "typed browser answer"}}}}
	done := remotePost(t, handler, false, event, operation)
	request := <-s.Native().Requests()
	answer := request.Operation.Answers.PublicAnswer()
	if answer == nil || answer.Call != "call" || len(answer.Answers) != 1 || answer.Answers[0].Text != "typed browser answer" {
		t.Fatalf("remote answer conversion: %+v", answer)
	}
	request.Reply <- ""
	if reply := <-done; reply.Code != 202 {
		t.Fatalf("remote answer refused: %+v", reply)
	}
}
