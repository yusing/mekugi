package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testExchange(client *Client, start, response context.Context, stream bool) (*http.Response, error) {
	return client.Responses(start, response, nil, http.Header{}, &WebSocketRequest{
		Payload: []byte(`{"type":"response.create","model":"gpt-test","input":[]}`), Headers: http.Header{}, Stream: stream,
	})
}

func TestProviderWebSocketLimitsAndShutdown(t *testing.T) {
	var connections atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"status":"in_progress"}}`))
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	client := New(upstream.URL, upstream.Client())
	client.EnableWebSockets(t.Context())
	defer client.Close()
	var responses []*http.Response
	for range providerWebSocketLimit {
		response, err := testExchange(client, t.Context(), t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if response, err := testExchange(client, deadline, t.Context(), false); err == nil {
		response.Body.Close()
		t.Fatal("pool exceeded capacity")
	}
	if connections.Load() != providerWebSocketLimit {
		t.Fatal("capacity wait started another handshake")
	}
	client.Close()
	for _, response := range responses {
		if _, err := io.ReadAll(response.Body); err == nil {
			t.Fatal("shutdown left in-flight response alive")
		}
		_ = response.Body.Close()
	}
	client.websockets.mu.Lock()
	remaining := len(client.websockets.entries)
	client.websockets.mu.Unlock()
	if remaining != 0 {
		t.Fatal("shutdown retained pool entries")
	}
}

func TestProviderWebSocketRetiresIdleAndOldConnections(t *testing.T) {
	for _, mode := range []string{"idle", "age"} {
		t.Run(mode, func(t *testing.T) {
			var connections atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connections.Add(1)
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for {
					if _, _, err = conn.Read(r.Context()); err != nil {
						return
					}
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
				}
			}))
			defer upstream.Close()
			client := New(upstream.URL, upstream.Client())
			client.EnableWebSockets(t.Context())
			defer client.Close()
			call := func() {
				response, err := testExchange(client, t.Context(), t.Context(), false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.ReadAll(response.Body); err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
			}
			call()
			var retired <-chan struct{}
			client.websockets.mu.Lock()
			for entry := range client.websockets.entries {
				retired = entry.done
				if mode == "idle" {
					entry.idleTimer.Reset(time.Millisecond)
				} else {
					entry.born = time.Now().Add(-providerWebSocketLifetime)
				}
			}
			client.websockets.mu.Unlock()
			if mode == "idle" {
				select {
				case <-retired:
				case <-time.After(5 * time.Second):
					t.Fatal("idle expiry did not close connection")
				}
			}
			call()
			if connections.Load() != 2 {
				t.Fatal("expired connection was reused")
			}
		})
	}
}
