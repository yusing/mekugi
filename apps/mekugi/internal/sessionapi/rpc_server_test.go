package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func rawControl(t *testing.T, handler http.Handler, native bool) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(Versioned(handler))
	t.Cleanup(server.Close)
	path := RPCPath
	if native {
		path = NativeRPCPath
	}
	conn, _, err := websocket.Dial(t.Context(), server.URL+path, &websocket.DialOptions{
		HTTPHeader: http.Header{ProtocolHeader: []string{ProtocolVersion}}, Subprotocols: []string{RPCSubprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func controlWrite(t *testing.T, conn *websocket.Conn, data string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func controlRead(t *testing.T, conn *websocket.Conn) RPCMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message RPCMessage
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func controlInitialize(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"version":"`+ProtocolVersion+`"}}`)
	if reply := controlRead(t, conn); reply.Error != nil {
		t.Fatalf("initialize: %+v", reply)
	}
}

func TestRPCServerInitializationAndRequestBoundary(t *testing.T) {
	for _, initial := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"version":"1"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"version":"` + ProtocolVersion + `","token":""}}`,
	} {
		t.Run(initial, func(t *testing.T) {
			hub := NewHub(t.Context(), nil)
			conn := rawControl(t, hub.Handler(), false)
			controlWrite(t, conn, initial)
			if response := controlRead(t, conn); response.Error == nil {
				t.Fatalf("initialization accepted: %+v", response)
			}
		})
	}
	hub := NewHub(t.Context(), nil)
	conn := rawControl(t, hub.Handler(), false)
	controlInitialize(t, conn)
	for _, invalid := range []string{
		`[{"jsonrpc":"2.0","id":2,"method":"session/interrupt","params":{}}]`,
		`{"jsonrpc":"2.0","method":"session/interrupt","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/interrupt","params":{},"result":null}`,
	} {
		controlWrite(t, conn, invalid)
		if response := controlRead(t, conn); response.Error == nil {
			t.Fatalf("invalid request accepted: %+v", response)
		}
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":3,"method":"native/start","params":{"subscription":"start","body":{"workspace":"/"}}}`)
	if response := controlRead(t, conn); response.Error == nil || response.Error.Data.Status != 403 {
		t.Fatalf("remote native admission: %+v", response)
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"redraw","method":"session/redraw","params":{}}`)
	if response := controlRead(t, conn); response.Error == nil || response.Error.Data.Status != 403 {
		t.Fatalf("remote native redraw admission: %+v", response)
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":4,"method":"session/list","params":{}}`)
	if response := controlRead(t, conn); response.Error != nil {
		t.Fatalf("connection unusable after refusal: %+v", response)
	}
}

func TestRPCServerAcknowledgesBeforeEarlyNativePublication(t *testing.T) {
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	conn := rawControl(t, hub.Handler(), true)
	controlInitialize(t, conn)
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"attach","method":"view/attach","params":{"thread":"thread","subscription":"view"}}`)
	request := <-s.Native().Requests()
	if err := s.Native().Publish(request.Attachment, nativeTestFrame("thread", 1)); err != nil {
		t.Fatal(err)
	}
	request.Reply <- ""
	ack := controlRead(t, conn)
	if string(ack.ID) != `"attach"` || ack.Error != nil {
		t.Fatalf("first message was not acknowledgement: %+v", ack)
	}
	var subscribed RPCSubscribed
	if err := json.Unmarshal(ack.Result, &subscribed); err != nil || subscribed.Subscription != "view" {
		t.Fatalf("subscription: %+v %v", subscribed, err)
	}
	notification := controlRead(t, conn)
	var event RPCEvent
	if err := json.Unmarshal(notification.Params, &event); err != nil || notification.Method != "subscription/event" || event.Subscription != "view" || event.Kind != "snapshot" {
		t.Fatalf("early publication: %+v %v", notification, err)
	}
	s.Native().Finish(nil)
	if message := controlRead(t, conn); json.Unmarshal(message.Params, &event) != nil || event.Kind != "exit" {
		t.Fatalf("joined exit: %+v", message)
	}
	if message := controlRead(t, conn); message.Method != "subscription/closed" {
		t.Fatalf("subscription termination: %+v", message)
	}
}

func TestRPCServerPreparationDoesNotBlockCancellationOrOtherMethods(t *testing.T) {
	started := make(chan context.Context, preparationLimit)
	s := NewPreparedLive(func(ctx context.Context, _ string, _ InputRequest) (*PreparedInput, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	conn := rawControl(t, hub.Handler(), false)
	controlInitialize(t, conn)
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"input","method":"session/input","params":{"thread":"thread","body":{"text":"once"}}}`)
	preparation := <-started
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"list","method":"session/list","params":{}}`)
	if reply := controlRead(t, conn); string(reply.ID) != `"list"` || reply.Error != nil {
		t.Fatalf("reader blocked behind preparation: %+v", reply)
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"cancel","method":"request/cancel","params":{"body":{"id":"input"}}}`)
	seen := make(map[string]RPCMessage)
	for range 2 {
		reply := controlRead(t, conn)
		seen[string(reply.ID)] = reply
	}
	if reply := seen[`"cancel"`]; reply.Error != nil || len(reply.Result) == 0 {
		t.Fatalf("cancellation result: %+v", reply)
	}
	if reply := seen[`"input"`]; reply.Error == nil || reply.Error.Data.Outcome != "not_admitted" {
		t.Fatalf("pre-admission cancellation: %+v", reply)
	}
	if preparation.Err() == nil {
		t.Fatal("preparation remained active after cancellation response")
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"interrupt","method":"session/interrupt","params":{"thread":"thread"}}`)
	intent := <-s.Intents()
	if intent.Action != Stop {
		t.Fatalf("unexpected owner operation: %+v", intent)
	}
	intent.Reply <- ""
	if reply := controlRead(t, conn); reply.Error != nil || string(reply.Result) != "null" {
		t.Fatalf("void operation result: %+v", reply)
	}
}

func TestRPCServerAggregatePreparationBudget(t *testing.T) {
	started := make(chan struct{}, preparationLimit)
	s := NewPreparedLive(func(ctx context.Context, _ string, _ InputRequest) (*PreparedInput, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	for range preparationLimit {
		conn := rawControl(t, hub.Handler(), false)
		controlInitialize(t, conn)
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"input","method":"session/input","params":{"thread":"thread","body":{"text":"once"}}}`)
		<-started
	}
	conn := rawControl(t, hub.Handler(), false)
	controlInitialize(t, conn)
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"overflow","method":"session/input","params":{"thread":"thread","body":{"text":"once"}}}`)
	if reply := controlRead(t, conn); reply.Error == nil || reply.Error.Data.Status != 503 || reply.Error.Data.Outcome != "not_admitted" {
		t.Fatalf("aggregate budget: %+v", reply)
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"list","method":"session/list","params":{}}`)
	if reply := controlRead(t, conn); reply.Error != nil {
		t.Fatalf("unrelated operation blocked: %+v", reply)
	}
}

func TestRPCServerPerMethodBodyLimit(t *testing.T) {
	hub := NewHub(t.Context(), nil)
	conn := rawControl(t, hub.Handler(), false)
	controlInitialize(t, conn)
	for _, method := range []string{"session/control", "session/shell", "session/answer", "session/decide", "session/interrupt"} {
		body := jsontext.Value(`{"text":"` + strings.Repeat("a", rpcMethods[method].limit) + `"}`)
		params, _ := json.Marshal(RPCParams{Body: body})
		request, _ := json.Marshal(RPCMessage{JSONRPC: "2.0", ID: jsontext.Value(`"limit"`), Method: method, Params: params})
		controlWrite(t, conn, string(request))
		if reply := controlRead(t, conn); reply.Error == nil || reply.Error.Data.Status != 413 {
			t.Fatalf("%s body limit: %+v", method, reply)
		}
	}
}

func TestRPCServerCloseJoinsUpgradedConnections(t *testing.T) {
	hub := NewHub(t.Context(), nil)
	client := openTestRPC(t, hub.Handler(), false)
	subscription, err := client.Subscribe(t.Context(), "session/subscribe", RPCParams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subscription.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	hub.mu.Lock()
	connections := len(hub.connections)
	hub.mu.Unlock()
	if connections != 0 {
		t.Fatalf("Close retained %d upgraded connections", connections)
	}
	if err := client.Call(t.Context(), "session/list", RPCParams{}, nil); err == nil {
		t.Fatal("closed server dispatched a request")
	}
}

func TestRPCServerRetirementDoesNotCloseReusedSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	old := newRPCSubscription(ctx, nil, nil)
	defer old.close()
	current := newRPCSubscription(ctx, nil, nil)
	defer current.close()
	connection := &rpcConnection{ctx: ctx, cancel: cancel, subscriptions: map[string]*rpcSubscription{"view": current}, responses: make(chan rpcReply, 1), wake: make(chan struct{}, 1)}
	connection.removeSubscription("view", old, rpcFailure(503, "old subscription closed"))
	if connection.subscriptions["view"] != current || current.ctx.Err() != nil || len(connection.responses) != 0 {
		t.Fatal("stale writer snapshot retired the replacement subscription")
	}
	connection.removeSubscription("view", current, nil)
	if connection.subscriptions["view"] != nil || current.ctx.Err() == nil || len(connection.responses) != 1 {
		t.Fatal("current subscription did not retire")
	}
}

func TestRPCServerShutdownDeliversJoinedExit(t *testing.T) {
	for range 30 {
		launcher := &nativeStartupLauncher{run: func(ctx context.Context, start Start, server *Server) error {
			request := <-server.Native().Requests()
			frame := nativeTestFrame("", 1)
			frame.View.Info.Workspace = start.Workspace
			if err := server.Native().Publish(request.Attachment, frame); err != nil {
				return err
			}
			request.Reply <- ""
			<-ctx.Done()
			return &NativeExit{Code: 17, Message: "joined shutdown outcome"}
		}}
		hub := NewHub(t.Context(), launcher)
		client, _ := serveControl(t, hub.Handler())
		view, err := StartNative(t.Context(), client, "http://mekugi", NativeStart{Workspace: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := view.Next(); err != nil {
			t.Fatal(err)
		}
		hub.Close()
		_, err = view.Next()
		outcome, joined := errors.AsType[*NativeExit](err)
		view.Close()
		if !joined || outcome.Code != 17 || outcome.Message != "joined shutdown outcome" {
			t.Fatalf("shutdown lost joined exit: %v", err)
		}
	}
}

func TestRPCServerShutdownBoundsBlockedPhysicalConnection(t *testing.T) {
	launcher := &nativeStartupLauncher{run: func(ctx context.Context, start Start, server *Server) error {
		request := <-server.Native().Requests()
		frame := nativeTestFrame("", 1)
		frame.View.Info.Workspace = start.Workspace
		frame.Facts.DebugDirectory = strings.Repeat("x", 8<<20)
		if err := server.Native().Publish(request.Attachment, frame); err != nil {
			return err
		}
		request.Reply <- ""
		<-ctx.Done()
		return &NativeExit{Code: 17, Message: "joined shutdown outcome"}
	}}
	hub := NewHub(t.Context(), launcher)
	// An unbuffered physical link makes the blocked writer deterministic.
	clientWire, serverWire := net.Pipe()
	listener := &rpcPipeListener{Conn: serverWire, done: make(chan struct{})}
	server := &http.Server{Handler: Versioned(hub.Handler())}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return clientWire, nil }}
	t.Cleanup(func() {
		clientWire.Close()
		server.Close()
		listener.Close()
		transport.CloseIdleConnections()
		<-served
	})
	conn, _, err := websocket.Dial(t.Context(), "http://mekugi"+NativeRPCPath, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport}, HTTPHeader: http.Header{ProtocolHeader: []string{ProtocolVersion}}, Subprotocols: []string{RPCSubprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	conn.SetReadLimit(RPCMessageLimit)
	controlInitialize(t, conn)
	body, _ := json.Marshal(NativeStart{Workspace: t.TempDir()})
	params, _ := json.Marshal(RPCParams{Subscription: "view", Body: body})
	request, _ := json.Marshal(RPCMessage{JSONRPC: "2.0", ID: jsontext.Value(`"start"`), Method: "native/start", Params: params})
	controlWrite(t, conn, string(request))
	if acknowledgement := controlRead(t, conn); acknowledgement.Error != nil || string(acknowledgement.ID) != `"start"` {
		t.Fatalf("startup acknowledgement: %+v", acknowledgement)
	}
	closed := make(chan struct{})
	go func() { hub.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(answerWithin + 2*time.Second):
		t.Fatal("shutdown did not join the blocked upgraded connection")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("blocked delivery was represented as a complete publication")
	}
	hub.mu.Lock()
	remaining := len(hub.connections)
	hub.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("shutdown retained %d connections", remaining)
	}
}

// rpcPipeListener exposes one unbuffered connection to the actual HTTP upgrader.
type rpcPipeListener struct {
	net.Conn
	done     chan struct{}
	once     sync.Once
	accepted bool
}

func (l *rpcPipeListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.Conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *rpcPipeListener) Close() error {
	l.once.Do(func() { close(l.done); l.Conn.Close() })
	return nil
}
func (l *rpcPipeListener) Addr() net.Addr { return l.Conn.LocalAddr() }

type drainOptionsLauncher struct {
	*managedLauncher
	started, release chan struct{}
	children         []context.CancelFunc
}

func (l *drainOptionsLauncher) StartOptions(ctx context.Context) (StartOptions, error) {
	// Fan-out holds request cancellation long enough for the writer to consume
	// its final reply first. Both dispatch and request retirement use production code.
	for range 20000 {
		_, cancel := context.WithCancel(ctx)
		l.children = append(l.children, cancel)
	}
	close(l.started)
	<-l.release
	return StartOptions{}, nil
}
func TestRPCDrainWakesAfterFinalRequestRetires(t *testing.T) {
	l := &drainOptionsLauncher{managedLauncher: newManagedLauncher(), started: make(chan struct{}), release: make(chan struct{})}
	hub := NewHub(t.Context(), l)
	client := openTestRPC(t, hub.Handler(), false)
	result := make(chan error, 1)
	go func() { result <- client.Call(t.Context(), "session/options", RPCParams{}, nil) }()
	<-l.started
	var connection *rpcConnection
	hub.mu.Lock()
	for c := range hub.connections {
		connection = c
	}
	hub.mu.Unlock()
	closed := make(chan struct{})
	go func() { hub.Close(); close(closed) }()
	deadline := time.Now().Add(time.Second)
	for {
		connection.mu.Lock()
		draining := connection.draining
		connection.mu.Unlock()
		if draining {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drain did not start")
		}
		time.Sleep(time.Millisecond)
	}
	close(l.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		connection.mu.Lock()
		remaining := len(connection.requests)
		connection.mu.Unlock()
		<-closed
		t.Fatalf("shutdown waited %s after final reply with %d pending requests", time.Since(start), remaining)
	}
}
