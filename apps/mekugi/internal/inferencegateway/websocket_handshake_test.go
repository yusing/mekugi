package inferencegateway

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestWebSocketReasoningHeader(t *testing.T) {
	for _, values := range [][]string{nil, {"true"}, {""}, {"false"}} {
		t.Run(fmt.Sprintf("values=%q", values), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if values != nil {
					w.Header()["X-Reasoning-Included"] = values
				}
				w.Header().Set("X-Unrelated", "private")
				if r.Header.Get("Session_id") != "session-affinity" {
					t.Error("lost handshake session affinity")
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				if _, _, err := conn.Read(ctx); err == nil {
					t.Error("handshake generated a provider request")
				}
			}))
			defer provider.Close()
			endpoint := NewWebSocketEndpoint(ctx, time.Second, NewClient(provider.URL, provider.Client(), codexresponses.Diagnostics{}), nil)
			defer endpoint.Close()
			server := httptest.NewServer(endpoint)
			defer server.Close()
			headers := http.Header{"Authorization": {"Bearer fixture"}, "Chatgpt-Account-Id": {"fixture"}, "Session-Id": {"session-affinity"}}
			conn, response, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: headers})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if got := response.Header.Values("X-Reasoning-Included"); !slices.Equal(got, values) {
				t.Fatalf("reasoning header = %q, want %q", got, values)
			}
			if response.Header.Get("X-Unrelated") != "" {
				t.Fatal("unrelated provider header leaked")
			}
		})
	}
}

func TestWebSocketMixedProviderHandshakeStaysLazy(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "OpenAI must not be contacted", http.StatusUnauthorized)
	}))
	defer provider.Close()
	client := NewClient(provider.URL, provider.Client(), codexresponses.Diagnostics{})
	client.Grok = &ThirdPartyClient{}
	endpoint := NewWebSocketEndpoint(t.Context(), time.Second, client, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, response, err := websocket.Dial(t.Context(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if calls.Load() != 0 || len(response.Header.Values("X-Reasoning-Included")) != 0 {
		t.Fatal("mixed-provider handshake acquired OpenAI state")
	}
}

func TestWebSocketHandshakeCancellation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%t", shutdown), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan struct{})
			stopped := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer provider.Close()
			timeout := 100 * time.Millisecond
			if shutdown {
				timeout = time.Minute
			}
			endpoint := NewWebSocketEndpoint(ctx, timeout, NewClient(provider.URL, provider.Client(), codexresponses.Diagnostics{}), nil)
			defer endpoint.Close()
			server := httptest.NewServer(endpoint)
			defer server.Close()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				headers := http.Header{"Authorization": {"Bearer fixture"}, "Chatgpt-Account-Id": {"fixture"}}
				conn, response, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: headers})
				if conn != nil {
					conn.CloseNow()
				}
				if response != nil && response.Body != nil {
					response.Body.Close()
				}
				if err == nil {
					t.Error("accepted a stalled provider handshake")
				}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("provider handshake did not start")
			}
			if shutdown {
				endpoint.Close()
			}
			for _, done := range []<-chan struct{}{finished, stopped} {
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("handshake cancellation did not release both connections")
				}
			}
		})
	}
}

func TestWebSocketRejectedUpgradeCapture(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusUpgradeRequired} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var encoded bytes.Buffer
			compressor := zlib.NewWriter(&encoded)
			_, _ = io.WriteString(compressor, `{"error":{"code":"fixture_rejection"}}`)
			if err := compressor.Close(); err != nil {
				t.Fatal(err)
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", "deflate")
				w.WriteHeader(status)
				_, _ = w.Write(encoded.Bytes())
			}))
			defer provider.Close()
			record, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			defer record.Close()
			endpoint := NewWebSocketEndpoint(t.Context(), time.Second, NewClient(provider.URL, provider.Client(), codexresponses.Diagnostics{}), nil)
			defer endpoint.Close()
			server := httptest.NewServer(record.Handler(endpoint))
			defer server.Close()
			headers := http.Header{"Authorization": {"Bearer fixture"}, "Chatgpt-Account-Id": {"fixture"}}
			conn, response, err := websocket.Dial(t.Context(), server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: headers})
			if conn != nil {
				conn.CloseNow()
			}
			if err == nil || response == nil || response.StatusCode != status {
				t.Fatalf("rejection lost: response=%v err=%v", response, err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.Header.Get("Content-Encoding") != "deflate" || !bytes.Equal(body, encoded.Bytes()) {
				t.Fatalf("encoded rejection changed: encoding=%q err=%v", response.Header.Get("Content-Encoding"), err)
			}
			snapshot := record.Snapshot()
			if status == http.StatusUpgradeRequired {
				if snapshot.Capture.Records != 0 || snapshot.Requests.Logical != 0 {
					t.Fatal("negotiation fallback invented an inference attempt")
				}
			} else if snapshot.Requests.ProviderAttempts != 1 || snapshot.Requests.Failed != 1 || snapshot.Capture.Incomplete != 0 ||
				snapshot.Transport.ProviderResponses.Bytes != uint64(encoded.Len()) || snapshot.Transport.ProviderAttemptRequests.Bytes != 0 {
				t.Fatalf("terminal rejection capture lost: requests=%+v capture=%+v transport=%+v", snapshot.Requests, snapshot.Capture, snapshot.Transport)
			}
		})
	}
}
