package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const leaseCompleted = `{"type":"response.completed","response":{"status":"completed","output":[]}}`

func TestProviderWebSocketStaleLeaseRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var connections atomic.Int32
	secondTerminal := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for n := 0; ; n++ {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.created"}`)); err != nil {
				return
			}
			if n == 1 {
				select {
				case <-secondTerminal:
				case <-ctx.Done():
					return
				}
			}
			if err := conn.Write(ctx, websocket.MessageText, []byte(leaseCompleted)); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	client := New(upstream.URL, upstream.Client())
	client.EnableWebSockets(ctx)
	defer client.Close()
	first, err := testExchange(client, ctx, ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	old := first.Body.(*WebSocketBody)
	if _, err := io.ReadAll(first.Body); err != nil {
		t.Fatal(err)
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := testExchange(client, ctx, ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	current := second.Body.(*WebSocketBody)
	if current.entry != old.entry || current.lease == old.lease {
		t.Fatal("successor must hold a new lease on the same connection")
	}
	if old.entry.release(old.lease, false) {
		t.Fatal("stale lease was released as current")
	}
	close(secondTerminal)
	raw, err := io.ReadAll(second.Body)
	if err != nil || !strings.Contains(string(raw), leaseCompleted) {
		t.Fatalf("successor response = %s, error = %v", raw, err)
	}
	if connections.Load() != 1 {
		t.Fatalf("connections = %d, want 1", connections.Load())
	}
}

func TestProviderWebSocketOversizedMessagePoisonsWithoutReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var connections, requests, httpRequests atomic.Int32
	sendOversized := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			httpRequests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		n := connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
			requests.Add(1)
			if n == 1 {
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.created"}`))
				select {
				case <-sendOversized:
				case <-ctx.Done():
					return
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"`+strings.Repeat("x", 512)+`"}`))
			} else {
				_ = conn.Write(ctx, websocket.MessageText, []byte(leaseCompleted))
			}
		}
	}))
	defer upstream.Close()
	client := New(upstream.URL, upstream.Client())
	client.EnableWebSockets(ctx)
	defer client.Close()
	first, err := testExchange(client, ctx, ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	body := first.Body.(*WebSocketBody)
	body.entry.conn.SetReadLimit(128)
	close(sendOversized)
	if _, err := io.ReadAll(first.Body); err == nil || !strings.Contains(err.Error(), "read limited") {
		t.Fatalf("oversized message error = %v, want read-limit failure", err)
	}
	_ = first.Body.Close()
	second, err := testExchange(client, ctx, ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.Body.(*WebSocketBody).entry == body.entry {
		t.Fatal("oversized message left the poisoned connection reusable")
	}
	if _, err := io.ReadAll(second.Body); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 2 || requests.Load() != 2 || httpRequests.Load() != 0 {
		t.Fatalf("connections/WS requests/HTTP requests = %d/%d/%d, want 2/2/0", connections.Load(), requests.Load(), httpRequests.Load())
	}
}

type leaseObservation struct {
	mu       sync.Mutex
	messages []string
	finishes []error
	atFinish []string
}

func (o *leaseObservation) Message(payload []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.messages = append(o.messages, string(payload))
}

func (o *leaseObservation) Finish(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finishes = append(o.finishes, err)
	o.atFinish = slices.Clone(o.messages)
}

func (*leaseObservation) HTTPErrorBody(body io.ReadCloser, _ int, _, _ string) io.ReadCloser {
	return body
}

func TestProviderWebSocketEarlyCloseObservesReservedTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const created = `{"type":"response.created"}`
	const delta = `{"type":"response.output_text.delta","delta":"queued"}`
	type received struct {
		connection int32
		payload    string
	}
	requests := make(chan received, 4)
	var connections atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, payload, err := conn.Read(ctx)
			if err != nil {
				return
			}
			requests <- received{n, string(payload)}
			if n == 1 {
				_ = conn.Write(ctx, websocket.MessageText, []byte(created))
				_ = conn.Write(ctx, websocket.MessageText, []byte(delta))
			}
			_ = conn.Write(ctx, websocket.MessageText, []byte(leaseCompleted))
		}
	}))
	defer upstream.Close()
	client := New(upstream.URL, upstream.Client())
	var observations []*leaseObservation
	var observedPayloads []string
	client.ObserveWebSocket = func(_ context.Context, payload []byte, _, _ http.Header) Observation {
		o := new(leaseObservation)
		observations = append(observations, o)
		observedPayloads = append(observedPayloads, string(payload))
		return o
	}
	client.EnableWebSockets(ctx)
	defer client.Close()
	first, err := testExchange(client, ctx, ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	body := first.Body.(*WebSocketBody)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		client.websockets.mu.Lock()
		reserved := body.lease.pending && body.lease.terminal && len(body.lease.messages) == 1
		client.websockets.mu.Unlock()
		if reserved {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("receiver did not reserve terminal behind queued message")
		}
	}
	_ = first.Body.Close()
	_ = first.Body.Close()
	firstObservation := observations[0]
	firstObservation.mu.Lock()
	wantMessages := []string{created, delta, leaseCompleted}
	if !slices.Equal(firstObservation.messages, wantMessages) || !slices.Equal(firstObservation.atFinish, wantMessages) || len(firstObservation.finishes) != 1 || !errors.Is(firstObservation.finishes[0], io.ErrUnexpectedEOF) {
		t.Errorf("early-close observation: messages=%q atFinish=%q finishes=%v", firstObservation.messages, firstObservation.atFinish, firstObservation.finishes)
	}
	firstObservation.mu.Unlock()
	const nextPayload = `{"type":"response.create","model":"gpt-test","input":[{"role":"user","content":"successor"}]}`
	second, err := client.Responses(ctx, ctx, nil, http.Header{}, &WebSocketRequest{Payload: []byte(nextPayload), Headers: http.Header{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(second.Body); err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	_ = second.Body.Close()
	var sent [2]received
	for i := range sent {
		select {
		case sent[i] = <-requests:
		case <-ctx.Done():
			t.Fatal("upstream did not receive both requests")
		}
	}
	if sent[0].connection == sent[1].connection || sent[0].payload == sent[1].payload || sent[1].payload != nextPayload || !slices.Equal(observedPayloads, []string{sent[0].payload, sent[1].payload}) {
		t.Fatalf("request/observation identity: sent=%+v observed=%q", sent, observedPayloads)
	}
	if len(observations) != 2 || observations[0] == observations[1] {
		t.Fatalf("observations = %d, want two distinct observations", len(observations))
	}
	secondObservation := observations[1]
	secondObservation.mu.Lock()
	defer secondObservation.mu.Unlock()
	if !slices.Equal(secondObservation.atFinish, []string{leaseCompleted}) || len(secondObservation.finishes) != 1 || secondObservation.finishes[0] != nil {
		t.Fatalf("successor observation: messages=%q finishes=%v", secondObservation.atFinish, secondObservation.finishes)
	}
}

func TestProviderWebSocketCancellationAfterReceiverStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(ctx); err != nil {
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.created"}`))
		_, _, _ = conn.Read(ctx)
	}))
	defer upstream.Close()
	client := New(upstream.URL, upstream.Client())
	client.EnableWebSockets(ctx)
	defer client.Close()
	responseCtx, cancelResponse := context.WithCancel(ctx)
	defer cancelResponse()
	response, err := testExchange(client, ctx, responseCtx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body := response.Body.(*WebSocketBody)
	cancelResponse()
	select {
	case <-body.entry.receiverDone:
	case <-ctx.Done():
		t.Fatal("cancellation did not stop receiver")
	}
	// Both entry.done and responseCtx are ready before Read selects a cause.
	if _, err := io.ReadAll(response.Body); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after receiver shutdown = %v", err)
	}
}
