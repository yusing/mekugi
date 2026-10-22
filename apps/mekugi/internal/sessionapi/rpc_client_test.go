package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func scriptedRPC(t *testing.T, script func(context.Context, *websocket.Conn)) *RPCClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{RPCSubprotocol}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		// Byte-pressure fixtures send more than 64 MiB through the actual
		// reader; race instrumentation needs more than the ordinary RPC budget.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		request := readScriptRPC(t, ctx, conn)
		if request.Method != "initialize" {
			t.Errorf("first method: %s", request.Method)
			return
		}
		writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: request.ID, Result: jsontext.Value(`{"version":"` + ProtocolVersion + `"}`)})
		script(ctx, conn)
	}))
	t.Cleanup(server.Close)
	client, err := OpenRPC(t.Context(), server.Client(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func readScriptRPC(t *testing.T, ctx context.Context, conn *websocket.Conn) RPCMessage {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Error(err)
		return RPCMessage{}
	}
	var request RPCMessage
	if err := json.Unmarshal(data, &request); err != nil {
		t.Error(err)
	}
	return request
}
func writeScriptRPC(t *testing.T, ctx context.Context, conn *websocket.Conn, message RPCMessage) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Error(err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Error(err)
	}
}

func TestRPCClientCorrelatesRepliesAndEarlyPublications(t *testing.T) {
	release := make(chan struct{})
	client := scriptedRPC(t, func(ctx context.Context, conn *websocket.Conn) {
		subscribe := readScriptRPC(t, ctx, conn)
		var params RPCParams
		if err := json.Unmarshal(subscribe.Params, &params); err != nil {
			t.Error(err)
			return
		}
		result, _ := json.Marshal(RPCSubscribed{Subscription: params.Subscription})
		writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: subscribe.ID, Result: result})
		event, _ := json.Marshal(RPCEvent{Subscription: params.Subscription, Kind: "snapshot", Data: jsontext.Value(`{"ready":true}`)})
		writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", Method: "subscription/event", Params: event})
		first := readScriptRPC(t, ctx, conn)
		second := readScriptRPC(t, ctx, conn)
		for _, request := range []RPCMessage{second, first} {
			result, _ := json.Marshal(request.Method)
			writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: request.ID, Result: result})
		}
		<-release
	})
	defer close(release)
	subscription, err := client.Subscribe(t.Context(), "view/attach", RPCParams{Thread: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	event, err := subscription.Next(t.Context())
	if err != nil || event.Kind != "snapshot" || string(event.Data) != `{"ready":true}` {
		t.Fatalf("early publication: %+v %v", event, err)
	}
	results := make(chan error, 2)
	for _, method := range []string{"session/read", "session/list"} {
		go func() {
			var got string
			err := client.Call(t.Context(), method, RPCParams{}, &got)
			if err == nil && got != method {
				err = errors.New("response correlated to the wrong method")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRPCClientCancellationAndUnknownDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "disconnect"}[disconnect], func(t *testing.T) {
			received := make(chan struct{})
			finished := make(chan struct{})
			client := scriptedRPC(t, func(ctx context.Context, conn *websocket.Conn) {
				defer close(finished)
				request := readScriptRPC(t, ctx, conn)
				close(received)
				if disconnect {
					return
				}
				cancellation := readScriptRPC(t, ctx, conn)
				var params RPCParams
				if err := json.Unmarshal(cancellation.Params, &params); err != nil {
					t.Error(err)
					return
				}
				var body struct {
					ID jsontext.Value `json:"id"`
				}
				if err := json.Unmarshal(params.Body, &body); err != nil || cancellation.Method != "request/cancel" || string(body.ID) != string(request.ID) {
					t.Errorf("cancellation identity: %+v %s %v", cancellation, body.ID, err)
				}
				writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: cancellation.ID, Result: jsontext.Value(`{}`)})
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- client.Call(ctx, "session/input", RPCParams{Thread: "thread", Body: jsontext.Value(`{"text":"once"}`)}, nil)
			}()
			<-received
			if !disconnect {
				cancel()
			}
			if err := <-result; err == nil || !strings.Contains(err.Error(), "unknown") {
				t.Fatalf("lost mutation outcome: %v", err)
			}
			<-finished
		})
	}
}

func TestNativeClientBacklogPreservesSwitchOutputAndEffects(t *testing.T) {
	release := make(chan struct{})
	client := scriptedRPC(t, func(ctx context.Context, conn *websocket.Conn) {
		request := readScriptRPC(t, ctx, conn)
		var params RPCParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Error(err)
			return
		}
		result, _ := json.Marshal(RPCSubscribed{Subscription: params.Subscription})
		writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: request.ID, Result: result})
		n := NewLive().Native()
		subscription, attach, failure := n.newView(ctx, requestTarget{workspace: "/workspace"}, "operation", localNativeView)
		if failure != nil {
			t.Error(failure)
			return
		}
		defer subscription.close()
		publish := func(event publication) {
			data, _ := json.Marshal(RPCEvent{Subscription: params.Subscription, Kind: event.Kind, Data: event.Data})
			writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", Method: "subscription/event", Params: data})
		}
		frame := nativeTestFrame("", 1)
		for i := range 81 {
			if i == 1 {
				frame = nativeTestFrame("confirmed", 2)
				frame.Switch = &NativeSwitch{To: "confirmed", Origin: attach.Attachment}
			} else {
				frame.Switch = nil
			}
			frame.View.Version.Revision = uint64(i + 1)
			frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: uint64(i + 1)}}}
			frame.AgentOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: uint64(i + 1)}}}
			if i == 2 || i == 41 {
				frame.Effects = append(frame.Effects, NativeEffect{ID: uint64(len(frame.Effects) + 1), Restore: &composer.Draft{Text: "retained input"}})
			}
			if err := n.Publish(attach.Attachment, frame); err != nil {
				t.Error(err)
				return
			}
			event, _ := subscription.native.pop()
			publish(event)
		}
		n.Finish(nil)
		event, _ := subscription.native.pop()
		publish(event)
		// The reply is a wire-order barrier: all publications reached the client
		// before the renderer starts consuming, while other RPCs remain usable.
		for ctx.Err() == nil {
			request := readScriptRPC(t, ctx, conn)
			writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: request.ID, Result: jsontext.Value(`{}`)})
			if request.Method == "session/read" {
				break
			}
		}
		<-release
	})
	defer close(release)
	subscription, err := client.Subscribe(t.Context(), "native/start", RPCParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Call(t.Context(), "session/read", RPCParams{Thread: "confirmed"}, nil); err != nil {
		t.Fatal(err)
	}
	connection := &NativeConnection{subscription: subscription, startup: "operation"}
	var last NativeEvent
	var mainOutput, agentOutput int
	for {
		event, err := connection.Next()
		if err != nil {
			if outcome, ok := errors.AsType[*NativeExit](err); !ok || outcome.Code != 0 {
				t.Fatalf("restoration backlog disconnected the native view: %v", err)
			}
			break
		}
		last = event
		for _, output := range event.Frame.MainOutput {
			mainOutput++
			if output.Demand.Source != uint64(mainOutput) {
				t.Fatalf("main output lost order: %d at %d", output.Demand.Source, mainOutput)
			}
		}
		for _, output := range event.Frame.AgentOutput {
			agentOutput++
			if output.Demand.Source != uint64(agentOutput) {
				t.Fatalf("agent output lost order: %d at %d", output.Demand.Source, agentOutput)
			}
		}
	}
	if last.Frame.View.Version.Revision != 81 || last.Frame.View.Main.ID != "confirmed" || len(last.Frame.Effects) != 2 || mainOutput != 81 || agentOutput != 81 {
		t.Fatalf("lost final state, switch, retained effects or acquired output: revision=%d thread=%q effects=%d mainOutput=%d agentOutput=%d", last.Frame.View.Version.Revision, last.Frame.View.Main.ID, len(last.Frame.Effects), mainOutput, agentOutput)
	}
}

// The response is a wire-order barrier: the reader must have accepted every
// queued observation before the independent control call can settle.
func bufferedRPCPublications(t *testing.T, method string, events []publication) (*RPCClient, *RPCSubscription) {
	t.Helper()
	release := make(chan struct{})
	client := scriptedRPC(t, func(ctx context.Context, conn *websocket.Conn) {
		request := readScriptRPC(t, ctx, conn)
		var params RPCParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Error(err)
			return
		}
		result, _ := json.Marshal(RPCSubscribed{Subscription: params.Subscription})
		writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: request.ID, Result: result})
		for _, event := range events {
			data, _ := json.Marshal(RPCEvent{Subscription: params.Subscription, Kind: event.Kind, Data: event.Data})
			writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", Method: "subscription/event", Params: data})
		}
		for ctx.Err() == nil {
			request := readScriptRPC(t, ctx, conn)
			writeScriptRPC(t, ctx, conn, RPCMessage{JSONRPC: "2.0", ID: request.ID, Result: jsontext.Value(`{}`)})
			if request.Method == "session/read" {
				break
			}
		}
		<-release
	})
	t.Cleanup(func() { close(release) })
	subscription, err := client.Subscribe(t.Context(), method, RPCParams{Thread: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Call(t.Context(), "session/read", RPCParams{Thread: "thread"}, nil); err != nil {
		t.Fatal("slow subscription blocked other control requests:", err)
	}
	return client, subscription
}
