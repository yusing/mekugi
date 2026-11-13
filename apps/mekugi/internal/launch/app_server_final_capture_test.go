package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/headless"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestHeadlessNativeCallbacksPrecedeInteractiveRejection(t *testing.T) {
	for _, interactive := range []string{"item/commandExecution/requestApproval", "future/interactiveRequest"} {
		t.Run(interactive, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var output bytes.Buffer
			stream := headless.NewStream(&output, cancel)
			entered, release := make(chan struct{}), make(chan struct{})
			u.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 1),
				foreign: func(ctx context.Context, _ appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
					close(entered)
					select {
					case <-release:
						return appserver.AgentForeignResponse{}, nil
					case <-ctx.Done():
						return appserver.AgentForeignResponse{}, ctx.Err()
					}
				},
			}
			t.Cleanup(u.finalCaptures.close)
			connection := &nativeConnection{root: &u.nativeSession, threads: &threadExecution{headless: stream}}
			observe := func(message appserver.Message) error {
				if err := stream.Observe(message); err != nil {
					return err
				}
				return connection.message(message)
			}
			request := appserver.Message{ID: jsontext.Value(`"foreign-route"`), Method: "agent/foreign/route", Params: jsontext.Value(`{}`)}
			if err := observe(request); err != nil {
				t.Fatal(err)
			}
			<-entered
			if ctx.Err() != nil || stream.Err() != nil || wire.Len() != 0 {
				t.Fatal("native callback became an interactive request or replied before settlement")
			}
			close(release)
			if err := u.finalCaptureReply(<-u.finalCaptures.results); err != nil {
				t.Fatal(err)
			}
			var reply appserver.Message
			if err := json.Unmarshal(wire.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(reply.ID, request.ID) || reply.Error != nil || len(reply.Result) == 0 {
				t.Fatalf("callback reply lost its native correlation: %s", wire.Bytes())
			}
			request = appserver.Message{ID: jsontext.Value(`7`), Method: interactive, Params: jsontext.Value(`{"threadId":"child"}`)}
			if err := observe(request); err == nil || !strings.Contains(err.Error(), "continue interactively") || ctx.Err() == nil {
				t.Fatalf("interactive request did not fail headless execution: %v", err)
			}
			rows := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			if len(rows) != 2 || !bytes.Contains(rows[0], []byte("agent/foreign/route")) || !bytes.Contains(rows[1], []byte(interactive)) {
				t.Fatalf("evidence was not serialized before native dispatch and rejection: %s", &output)
			}
		})
	}
}

func TestNativeHostAdmissionIsAwaitedAndRepliesOnlyOnConnectionLoop(t *testing.T) {
	u, wire := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	u.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 1), admit: func(ctx context.Context, request appserver.ChildHostAdmissionRequest) error {
		if request.ThreadID != "child" || request.ParentThreadID != "parent" || request.LocalCwd != nil {
			t.Errorf("admission changed native identity or unavailable cwd: %+v", request)
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	t.Cleanup(u.finalCaptures.close)
	handled, err := u.finalCaptureMessage(appserver.Message{ID: []byte(`"admission-1"`), Method: "agent/hostAdmission/prepare", Params: []byte(`{"rootThreadId":"root","parentThreadId":"parent","threadId":"child","localCwd":null}`)})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	<-entered
	if wire.Len() != 0 {
		t.Fatal("admission replied before resource preparation settled")
	}
	close(release)
	reply := <-u.finalCaptures.results
	if wire.Len() != 0 {
		t.Fatal("admission worker wrote to the native connection")
	}
	if err := u.finalCaptureReply(reply); err != nil {
		t.Fatal(err)
	}
	var message appserver.Message
	if err := json.Unmarshal(wire.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if string(message.ID) != `"admission-1"` || string(message.Result) != `{"threadId":"child"}` {
		t.Fatalf("lost native admission correlation: %s", wire.Bytes())
	}
}

func TestNativeProgressCallbacksReplyOnOwningLoop(t *testing.T) {
	u, wire := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	called := make(chan string, 1)
	u.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 1),
		freeze: func(context.Context, appserver.ProgressFreezeRequest) (appserver.ProgressFreezeResponse, error) {
			called <- "agent/progress/freeze"
			return appserver.ProgressFreezeResponse{}, nil
		},
		output: func(context.Context, appserver.ProgressOutputRequest) (appserver.ProgressOutputResponse, error) {
			called <- "agent/progress/prepareOutput"
			return appserver.ProgressOutputResponse{}, nil
		},
		project: func(context.Context, appserver.FinalCaptureProjectRequest) (appserver.FinalCaptureProjectResponse, error) {
			called <- "agent/finalCapture/project"
			return appserver.FinalCaptureProjectResponse{}, nil
		},
	}
	t.Cleanup(u.finalCaptures.close)
	for _, method := range []string{"agent/progress/freeze", "agent/progress/prepareOutput", "agent/finalCapture/project"} {
		wire.Reset()
		handled, err := u.finalCaptureMessage(appserver.Message{ID: []byte(`"callback-original-id"`), Method: method, Params: []byte(`{}`)})
		if !handled || err != nil {
			t.Fatal(handled, err)
		}
		if actual := <-called; actual != method {
			t.Fatalf("callback %q reached %q", method, actual)
		}
		reply := <-u.finalCaptures.results
		if wire.Len() != 0 {
			t.Fatal("resource worker wrote to connection")
		}
		if err := u.finalCaptureReply(reply); err != nil {
			t.Fatal(err)
		}
		var message appserver.Message
		if err := json.Unmarshal(wire.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		if string(message.ID) != `"callback-original-id"` || message.Error != nil || len(message.Result) == 0 {
			t.Fatalf("resource reply lost correlation: %s", wire.Bytes())
		}
	}
}

func TestNativeFinalCaptureRepliesOnEventLoopWithOriginalID(t *testing.T) {
	u, wire := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	started, release := make(chan struct{}), make(chan struct{})
	u.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 1), prepare: func(ctx context.Context, request appserver.FinalCaptureRequest) (*appserver.FrozenFinalCapture, error) {
		if request.RecipientThreadID != "parent" || len(request.Sources) != 2 {
			t.Errorf("wrong capture attribution: %+v", request)
		}
		close(started)
		select {
		case <-release:
			return &appserver.FrozenFinalCapture{ResourceRef: "retained-resource", FullText: "frozen evidence"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	t.Cleanup(u.finalCaptures.close)
	handled, err := u.finalCaptureMessage(appserver.Message{ID: []byte(`"native-42"`), Method: "agent/finalCapture/prepare", Params: []byte(`{"rootThreadId":"root","threadId":"child","workId":"work","recipientThreadId":"parent","recipientCwd":"/workspace","sources":["mchanges:amber1","mchanges:birch1"]}`)})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	<-started
	if wire.Len() != 0 {
		t.Fatal("capture worker wrote to connection or blocked admission")
	}
	close(release)
	if err := u.finalCaptureReply(<-u.finalCaptures.results); err != nil {
		t.Fatal(err)
	}
	var reply appserver.Message
	if err := json.Unmarshal(wire.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if string(reply.ID) != `"native-42"` || string(reply.Result) != `{"capture":{"resourceRef":"retained-resource","fullText":"frozen evidence"}}` {
		t.Fatalf("capture reply = %s", wire.Bytes())
	}
}

func TestNativeFinalCaptureShutdownCancelsAndJoinsAcquisition(t *testing.T) {
	u, wire := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	started, joined := make(chan struct{}), make(chan struct{})
	u.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 1), prepare: func(ctx context.Context, _ appserver.FinalCaptureRequest) (*appserver.FrozenFinalCapture, error) {
		close(started)
		<-ctx.Done()
		close(joined)
		return nil, ctx.Err()
	}}
	if handled, err := u.finalCaptureMessage(appserver.Message{ID: []byte(`1`), Method: "agent/finalCapture/prepare", Params: []byte(`{"sources":[]}`)}); !handled || err != nil {
		t.Fatal(handled, err)
	}
	<-started
	u.finalCaptures.close()
	select {
	case <-joined:
	default:
		t.Fatal("shutdown returned before capture acquisition joined")
	}
	if wire.Len() != 0 {
		t.Fatal("shutdown worker wrote a late response")
	}
}

func TestNativeHostCallbackCancellationIsExactAndSuppressesLateReply(t *testing.T) {
	u, wire := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan string, 2)
	stopped := make(chan string, 2)
	release := make(chan struct{})
	u.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 2), prepare: func(ctx context.Context, request appserver.FinalCaptureRequest) (*appserver.FrozenFinalCapture, error) {
		started <- request.ThreadID
		select {
		case <-ctx.Done():
			stopped <- request.ThreadID
			return nil, ctx.Err()
		case <-release:
			return &appserver.FrozenFinalCapture{ResourceRef: "resource", FullText: "capture"}, nil
		}
	}}
	t.Cleanup(u.finalCaptures.close)
	for _, id := range []string{"first", "second"} {
		handled, err := u.finalCaptureMessage(appserver.Message{ID: []byte(`"` + id + `"`), Method: "agent/finalCapture/prepare", Params: []byte(`{"threadId":"` + id + `"}`)})
		if !handled || err != nil {
			t.Fatal(handled, err)
		}
	}
	<-started
	<-started
	handled, err := u.finalCaptureMessage(appserver.Message{Method: "serverRequest/resolved", Params: []byte(`{"requestId":"first"}`)})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if id := <-stopped; id != "first" {
		t.Fatalf("canceled sibling %q", id)
	}
	if err := u.finalCaptureReply(finalCaptureReply{id: []byte(`"first"`), capture: &appserver.FrozenFinalCapture{FullText: "late"}}); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 {
		t.Fatal("retired callback wrote a late reply")
	}
	if len(u.finalCaptures.pending) != 1 || u.finalCaptures.pending[`"second"`] == nil {
		t.Fatal("callback cancellation crossed request ownership")
	}
	close(release)
	for {
		reply := <-u.finalCaptures.results
		if err := u.finalCaptureReply(reply); err != nil {
			t.Fatal(err)
		}
		if string(reply.id) == `"second"` {
			break
		}
	}
	var result appserver.Message
	if err := json.Unmarshal(wire.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if string(result.ID) != `"second"` || result.Error != nil {
		t.Fatalf("sibling callback did not survive cancellation: %s", wire.Bytes())
	}
}
