package sessionapi

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type managedLauncher struct {
	count   atomic.Int32
	started chan *Server
	inputs  chan Intent
	stopped chan struct{}
	ready   <-chan struct{}
}

func (l *managedLauncher) StartOptions(context.Context) (StartOptions, error) {
	return StartOptions{Models: []ModelGroup{{Models: []Model{{ID: "codex"}}}}}, nil
}
func (l *managedLauncher) PrepareInput(_ context.Context, _ string, input InputRequest) (*PreparedInput, error) {
	return NewPreparedInput(composer.Draft{Text: input.Text}, nil), nil
}
func (l *managedLauncher) Run(ctx context.Context, start Start, s *Server) error {
	n := l.count.Add(1)
	l.started <- s
	if l.ready != nil {
		select {
		case <-l.ready:
		case <-ctx.Done():
			l.stopped <- struct{}{}
			return ctx.Err()
		}
	}
	id := fmt.Sprintf("logical-%d", n)
	if start.Resume != "" {
		id = start.Resume
	}
	if err := s.SetSummary(Summary{Thread: id, Workspace: start.Workspace, Ready: true, Started: time.Now()}); err != nil {
		return err
	}
	if err := s.Publish(Frame{Session: Session{Thread: id, Workspace: start.Workspace}}); err != nil {
		return err
	}
	defer func() { l.stopped <- struct{}{} }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case in := <-s.Intents():
			if in.Context != nil && in.Context.Err() != nil {
				in.Reply <- "Canceled"
				continue
			}
			if in.Prepared != nil && !in.Prepared.Admit(in.Context, make(map[string]bool)) {
				in.Reply <- "Canceled"
				continue
			}
			l.inputs <- in
			in.Reply <- ""
		}
	}
}
func newManagedLauncher() *managedLauncher {
	return &managedLauncher{started: make(chan *Server, 4), inputs: make(chan Intent, 8), stopped: make(chan struct{}, 4)}
}

func TestManagedActiveThreadResolutionUsesOwnedRegistry(t *testing.T) {
	t.Parallel()
	launcher := newManagedLauncher()
	hub := NewHub(t.Context(), launcher)
	defer hub.Close()
	response := managedRequest(t, hub, "session/start", "", Start{Workspace: t.TempDir(), Model: "codex", Text: "first"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("create: %d: %s", response.Code, response.Body)
	}
	server := <-launcher.started
	if !server.ActiveThread("logical-1") || server.ActiveThread("unknown") {
		t.Fatal("active lookup did not distinguish the owned runtime from missing publications")
	}
	response = managedRequest(t, hub, "session/end", "logical-1", nil)
	if response.Code != http.StatusAccepted || server.ActiveThread("logical-1") {
		t.Fatal("ended runtime remained a live native attachment destination")
	}
}

type testReply struct {
	Code int
	Body *bytes.Buffer
}

func ownerReply(t *testing.T, result any, failure *RPCError) testReply {
	t.Helper()
	if failure != nil {
		return testReply{failure.Data.Status, bytes.NewBufferString(failure.Message)}
	}
	body, err := json.Marshal(result, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	return testReply{http.StatusAccepted, bytes.NewBuffer(body)}
}

func managedRequest(t *testing.T, hub *Hub, method, thread string, value any) testReply {
	t.Helper()
	if method == "session/start" {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		result, failure := hub.startSession(t.Context(), body)
		return ownerReply(t, result, failure)
	}
	client := openTestRPC(t, hub.Handler(), false)
	return client.call(t, method, RPCParams{Thread: thread, Body: testJSON(t, value)})
}

func testJSON(t *testing.T, value any) jsontext.Value {
	t.Helper()
	body, err := json.Marshal(value, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type testRPC struct{ *RPCClient }

func openTestRPC(t *testing.T, handler http.Handler, native bool) *testRPC {
	t.Helper()
	host := httptest.NewServer(Versioned(handler))
	t.Cleanup(host.Close)
	path := RPCPath
	if native {
		path = NativeRPCPath
	}
	client, err := OpenRPC(t.Context(), host.Client(), "ws"+strings.TrimPrefix(host.URL, "http")+path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return &testRPC{client}
}

func (c *testRPC) call(t *testing.T, method string, params RPCParams) testReply {
	t.Helper()
	var result jsontext.Value
	err := c.Call(t.Context(), method, params, &result)
	if err != nil {
		var failure *RPCError
		if !errors.As(err, &failure) {
			t.Fatal(err)
		}
		return ownerReply(t, nil, failure)
	}
	return testReply{http.StatusAccepted, bytes.NewBuffer(result)}
}

func TestManagedRuntimeInputAndClientDisconnect(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	launcher := newManagedLauncher()
	hub := NewHub(ctx, launcher)
	defer hub.Close()
	start := Start{Workspace: t.TempDir(), Model: "codex", Text: "first"}
	rec := managedRequest(t, hub, "session/start", "", start)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), "logical-1") {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	first := <-launcher.inputs
	if first.Text != "first" || first.Thread != "logical-1" || first.Workspace != start.Workspace {
		t.Fatalf("first input: %+v", first)
	}
	viewer := openTestRPC(t, hub.Handler(), false)
	if _, err := viewer.Subscribe(t.Context(), "session/subscribe", RPCParams{Thread: "logical-1"}); err != nil {
		t.Fatal(err)
	}
	viewer.Close()
	select {
	case <-launcher.stopped:
		t.Fatal("viewer stopped execution")
	default:
	}
	rec = managedRequest(t, hub, "session/input", "logical-1", map[string]string{"text": "later"})
	if rec.Code != 202 {
		t.Fatalf("later: %d %s", rec.Code, rec.Body)
	}
	later := <-launcher.inputs
	if later.Thread != first.Thread || later.Workspace != first.Workspace || later.Text != "later" {
		t.Fatalf("later input: %+v", later)
	}
	rec = managedRequest(t, hub, "session/end", "logical-1", nil)
	if rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	select {
	case <-launcher.stopped:
	case <-ctx.Done():
		t.Fatal("end did not stop runtime")
	}
}
func TestManagedCreationCancellationAndResumeReservation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	launcher := newManagedLauncher()
	launcher.ready = ready
	hub := NewHub(ctx, launcher)
	defer hub.Close()
	start := Start{Workspace: t.TempDir(), Model: "codex", Resume: "saved-thread"}
	body, _ := json.Marshal(start)
	requestCtx, disconnect := context.WithCancel(ctx)
	returned := make(chan struct{})
	go func() { hub.startSession(requestCtx, body); close(returned) }()
	select {
	case <-launcher.started:
	case <-ctx.Done():
		t.Fatal("startup never ran")
	}
	disconnect()
	<-returned
	duplicate := managedRequest(t, hub, "session/start", "", start)
	if duplicate.Code != 409 || launcher.count.Load() != 1 {
		t.Fatalf("duplicate: %d, launches %d", duplicate.Code, launcher.count.Load())
	}
	close(ready)
	hub.Close()
	select {
	case <-launcher.stopped:
	default:
		t.Fatal("Close did not join owned runtime")
	}
}

// Runtime cancellation cannot acknowledge pending input as admitted.
func TestRuntimeEndDoesNotAcknowledgePendingInput(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			api := NewLive()
			runtimeCtx, end := context.WithCancel(ctx)
			defer end()
			hub := NewHub(ctx, nil)
			hub.sessions["thread"] = &listedSession{entry: listed{ID: "thread", Thread: "thread", Workspace: "/workspace"}, server: api, runtime: &managedSession{ctx: runtimeCtx, cancel: end}}
			client := openTestRPC(t, hub.Handler(), false)
			result := make(chan testReply, 1)
			go func() {
				result <- client.call(t, "session/input", RPCParams{Thread: "thread", Body: []byte(`{"text":"later"}`)})
			}()
			if dispatched {
				select {
				case <-api.Intents():
				case <-ctx.Done():
					t.Fatal("input not dispatched")
				}
			}
			end()
			select {
			case reply := <-result:
				if reply.Code != http.StatusServiceUnavailable {
					t.Fatalf("unacknowledged input returned %v", reply)
				}
			case <-ctx.Done():
				t.Fatal("canceled input did not finish")
			}
		})
	}
}
