package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/inference"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"

	"github.com/yusing/mekugi/libraries/httptransport"

	"github.com/yusing/mekugi/providers/grok"

	"github.com/coder/websocket"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestProviderWebSocketReuseAndMetadata(t *testing.T) {
	var connections atomic.Int32
	requests := make(chan map[string]json.RawMessage, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		w.Header().Set("x-request-id", "handshake-only")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request map[string]json.RawMessage
			if err := json.Unmarshal(payload, &request); err != nil {
				t.Error(err)
				return
			}
			requests <- request
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`)); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	headers := http.Header{"Authorization": {"Bearer test"}, openai.AccountIDHeader: {"account"}}
	headers.Set(codexresponses.ThreadIDHeader, "thread")
	for _, turn := range []string{"one", "two"} {
		headers.Set("x-codex-turn-state", turn)
		headers.Set(codexresponses.TurnMetadataHeader, turn)
		response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":true,"input":[]}`), headers, "session")
		if err != nil {
			t.Fatal(err)
		}
		if (turn == "one" && response.Header.Get("x-request-id") != "handshake-only") || (turn == "two" && response.Header.Get("x-request-id") != "") {
			t.Fatal("handshake evidence leaked between exchanges")
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 {
			t.Fatal("missing terminal event")
		}
		sent := <-requests
		if string(sent["type"]) != `"response.create"` || string(sent["input"]) != `[]` {
			t.Fatalf("unexpected request %s", sent)
		}
		if _, ok := sent["stream"]; ok {
			t.Fatal("HTTP stream field sent over websocket")
		}
		var metadata map[string]string
		if err := json.Unmarshal(sent["client_metadata"], &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["x-codex-turn-state"] != turn || metadata[codexresponses.TurnMetadataHeader] != turn {
			t.Fatalf("stale metadata: %v", metadata)
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("got %d connections, want one", connections.Load())
	}
}

func TestProviderWebSocketPoisonedLeasesAreNotReplayed(t *testing.T) {
	for _, scenario := range []string{"cancel", "close during read", "EOF", "invalid JSON", "idle timeout"} {
		t.Run(scenario, func(t *testing.T) {
			var connections, sends atomic.Int32
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				number := connections.Add(1)
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err = conn.Read(r.Context()); err != nil {
					return
				}
				sends.Add(1)
				if number == 1 {
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"status":"in_progress"}}`))
					<-release
					switch scenario {
					case "invalid JSON":
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`not JSON`))
					}
					return
				}
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.openai.EnableWebSockets(t.Context())
			defer client.openai.Close()
			if scenario == "idle timeout" {
				client.openai.StreamIdleTimeout = 20 * time.Millisecond
			}
			responseCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			response, err := client.forwardExecution(t.Context(), responseCtx, []byte(`{"model":"gpt-test","stream":true,"input":[]}`), codexAuthHeaders(), "session")
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			switch scenario {
			case "idle timeout": // Leave the provider connected but silent.
			case "cancel":
				cancel()
				close(release)
			case "close during read":
				readDone := make(chan error, 1)
				go func() { _, err := io.ReadAll(response.Body); readDone <- err }()
				_ = response.Body.Close()
				select {
				case err := <-readDone:
					if err == nil {
						t.Error("early close accepted incomplete stream")
					}
				case <-time.After(5 * time.Second):
					t.Error("Close did not unblock Read")
				}
				close(release)
			default:
				close(release)
			}
			if scenario != "close during read" {
				if scenario == "cancel" {
					_, err = copyUpstreamBodyTransformed(io.Discard, response, true, nil, nil)
				} else {
					_, err = io.ReadAll(response.Body)
				}
				if err == nil {
					t.Fatal("incomplete websocket stream succeeded")
				}
				if scenario == "cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("request cancellation lost its cause: %v", err)
					}
					issues := testCriticalErrors()
					f := &requestFinalization{}
					f.classifyCopyError(err)
					f.finish(responseCtx, err, &trackedResponseWriter{committed: true}, issues)
					if f.observation.outcome != requestOutcomeCanceledAfterResponse || len(issues.Notices.Pending()) != 0 {
						t.Fatalf("host interruption reported as failure: outcome=%v notices=%v", f.observation.outcome, issues.Notices.Pending())
					}
				}
				if scenario == "idle timeout" {
					close(release)
					if !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
						t.Fatalf("idle timeout lost classification: %v", err)
					}
				}
				_ = response.Body.Close()
			}
			next, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":true,"input":[]}`), codexAuthHeaders(), "session")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadAll(next.Body); err != nil {
				t.Fatal(err)
			}
			_ = next.Body.Close()
			if connections.Load() != 2 || sends.Load() != 2 {
				t.Fatalf("ambiguous request replayed or connection reused: connections=%d sends=%d", connections.Load(), sends.Load())
			}
		})
	}
}

func TestProviderWebSocketHandshakeFallbackBoundary(t *testing.T) {
	for _, status := range []int{404, 405, 501, 401, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var gets, posts atomic.Int32
			rejectedBody := `{"error":{"message":"` + strings.Repeat("upgrade rejected", 2048) + `"}}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
					w.Header().Set("Retry-After", "9")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, rejectedBody)
					return
				}
				posts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":"completed","output":[]}`)
			}))
			defer upstream.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.openai.EnableWebSockets(t.Context())
			defer client.openai.Close()
			response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","input":[]}`), codexAuthHeaders(), "session")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			fallback := status == 404 || status == 405 || status == 501
			if fallback {
				if response.StatusCode != 200 || posts.Load() != 1 {
					t.Fatal("unsupported upgrade did not fall back")
				}
			} else if response.StatusCode != status || posts.Load() != 0 || response.Header.Get("Retry-After") != "9" {
				t.Fatal("provider error hidden by retry/fallback")
			}
			if !fallback && string(body) != rejectedBody {
				t.Fatalf("rejected upgrade body truncated: got %d bytes want %d", len(body), len(rejectedBody))
			}
			if gets.Load() != 1 {
				t.Fatalf("unexpected handshake retry: %d", gets.Load())
			}
		})
	}
}

func TestProviderWebSocketConcurrentAndAuthIsolation(t *testing.T) {
	var connections atomic.Int32
	accepted := make(chan string, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, _, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			accepted <- r.Header.Get("Authorization")
			<-release
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
		}
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	done := make(chan error, 2)
	run := func(headers http.Header) {
		response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","input":[]}`), headers, "same-session")
		if err == nil {
			_, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		done <- err
	}
	for range 2 {
		go run(codexAuthHeaders())
	}
	for range 2 {
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("concurrent request stalled")
		}
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 2 {
		t.Fatalf("concurrent requests shared an active socket: %d", connections.Load())
	}
	different := codexAuthHeaders()
	different.Set("Authorization", "Bearer other-account")
	run(different)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := <-accepted; got != "Bearer other-account" || connections.Load() != 3 {
		t.Fatal("credentials crossed connection boundary")
	}
}

func TestProviderWebSocketNonstreamReconstructsOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		for _, payload := range []string{
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call","name":"lookup","arguments":"{}"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg","content":[{"type":"output_text","text":"hello"}]}}`,
			`{"type":"response.completed","response":{"id":"resp","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`,
		} {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(payload)); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":false,"input":[]}`), codexAuthHeaders(), "session")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	transform := &webSocketCountingTransform{}
	usage := inference.Usage{}
	state, err := copyUpstreamBodyTransformed(&output, response, false, transform, &responseHooks{onUsage: func(counts inference.Usage) { usage = counts }})
	if err != nil {
		t.Fatal(err)
	}
	var terminal struct {
		Status string `json:"status"`
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	}
	if err := json.Unmarshal(output.Bytes(), &terminal); err != nil {
		t.Fatal(err)
	}
	if state != responseevents.TerminalCompleted || len(terminal.Output) != 2 || terminal.Output[0].Type != "message" || terminal.Output[1].Type != "function_call" || transform.jsonCalls != 1 || transform.streamCalls != 0 || transform.finishes != 1 || usage.InputTokens != 5 {
		t.Fatalf("nonstream contract failed: %s %+v %+v", output.String(), transform, usage)
	}
}

type webSocketCountingTransform struct{ jsonCalls, streamCalls, finishes int }

func (c *webSocketCountingTransform) TransformJSON(payload []byte) ([]byte, error) {
	c.jsonCalls++
	return payload, nil
}
func (c *webSocketCountingTransform) TransformSSE(payload []byte) ([][]byte, error) {
	c.streamCalls++
	return [][]byte{payload}, nil
}
func (c *webSocketCountingTransform) Finish(bool) error { c.finishes++; return nil }

func TestProviderWebSocketWrappedErrorPreservesStatusAndHeaders(t *testing.T) {
	var sends atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		sends.Add(1)
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"error","status":429,"headers":{"retry-after":7,"x-request-id":"error-id","Connection":"upgrade","bad\r\nname":"bad"},"error":{"message":"rate limited"}}`))
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":true,"input":[]}`), codexAuthHeaders(), "session")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 429 || response.Header.Get("Retry-After") != "7" || response.Header.Get("x-request-id") != "error-id" || response.Header.Get("Connection") != "" || !bytes.Contains(body, []byte("rate limited")) || sends.Load() != 1 {
		t.Fatalf("error contract changed: status=%d headers=%v err=%v", response.StatusCode, response.Header, err)
	}
}

func TestRunSessionUsesProviderWebSocketsByDefault(t *testing.T) {
	var connections atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Upgrade") != "websocket" {
			t.Error("default provider did not upgrade")
			w.WriteHeader(500)
			return
		}
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
	originalTransport := http.DefaultTransport
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&tls.Dialer{Config: transport.TLSClientConfig}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = originalTransport; transport.CloseIdleConnections() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--mode", "passthrough"}, nil, func(session Session) { ready <- session.BaseURL }, nil)
	}()
	var baseURL string
	select {
	case baseURL = <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no readiness")
	}
	for range 2 {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/responses", strings.NewReader(`{"model":"gpt-test","stream":true,"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header = codexAuthHeaders()
		request.Header.Set(codexresponses.ThreadIDHeader, "thread")
		request.Header.Set(sessionIDHeader, "session")
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte("response.completed")) {
			t.Fatalf("default WS response: status=%d err=%v body=%s", response.StatusCode, err, body)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("router shutdown stalled")
	}
	if connections.Load() != 1 {
		t.Fatalf("default session failed to reuse: %d", connections.Load())
	}
}

func TestWebSocketRequestPreservesRichBodyMetadata(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set(codexresponses.TurnMetadataHeader, "bounded header metadata")
	headers.Set("x-codex-turn-state", "current state")
	payload, handshake, _, err := codexresponses.WebSocketRequest([]byte(`{"model":"model","input":[],"client_metadata":{"x-codex-turn-metadata":"full body metadata","custom":"preserved"}}`), headers, "session")
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Metadata map[string]string `json:"client_metadata"`
	}
	if err := json.Unmarshal(payload, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Metadata[codexresponses.TurnMetadataHeader] != "full body metadata" || sent.Metadata["custom"] != "preserved" || sent.Metadata["x-codex-turn-state"] != "current state" || handshake.Get(codexresponses.TurnMetadataHeader) != "" || handshake.Get("x-codex-turn-state") != "" {
		t.Fatal("body metadata overwritten or per-turn metadata retained in handshake")
	}
}

func TestWebSocketCapturePreservesCompletedResponseAfterCancellation(t *testing.T) {
	terminal := []byte(`{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":3}}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, terminal)
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	capture, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	handler := capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		payload := []byte(`{"model":"model","stream":true,"input":[]}`)
		transportcapture.ObserveProjectedRequest(ctx, payload)
		response, err := client.forwardExecution(ctx, ctx, payload, codexAuthHeaders(), "session")
		if err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Error(err)
		}
		// The caller can end the request after consuming its terminal response
		// but before the deferred body close finalizes provider capture.
		cancel()
		_ = response.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(data)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","stream":true,"input":[]}`)))
	metrics := httptest.NewRecorder()
	capture.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	var snapshot struct {
		Capture struct {
			Errors     uint64 `json:"capture_errors"`
			Incomplete uint64 `json:"incomplete_records"`
		} `json:"capture"`
	}
	if err := json.Unmarshal(metrics.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Capture.Errors != 0 || snapshot.Capture.Incomplete != 0 {
		t.Fatalf("completed response became a failed capture after cancellation: %+v", snapshot.Capture)
	}
}

func TestWebSocketCaptureCountsFirstUnreadMessage(t *testing.T) {
	first := []byte(`{"type":"response.created","response":{"status":"in_progress"}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, first)
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	capture, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	handler := capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, err := client.forwardExecution(r.Context(), r.Context(), []byte(`{"model":"model","stream":true,"input":[]}`), codexAuthHeaders(), "session")
		if err != nil {
			t.Error(err)
			return
		}
		_ = response.Body.Close()
		w.WriteHeader(502)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","stream":true,"input":[]}`)))
	metrics := httptest.NewRecorder()
	capture.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	var snapshot struct {
		Transport struct {
			Response struct {
				Bytes uint64 `json:"bytes"`
			} `json:"provider_responses"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(metrics.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Transport.Response.Bytes != uint64(len(first)) {
		t.Fatal("already received first message was omitted from capture")
	}
}

func TestProviderWebSocketLeavesGrokOnHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Upgrade") != "" || r.Header.Get("Authorization") != "Bearer grok-test" {
			t.Error("Grok transport/auth changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, grokTextStream())
	}))
	defer upstream.Close()
	client := newProviderClient("http://127.0.0.1:1", nil)
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	client.grok = &grokClient{httpClient: grokTestHTTPClient(t, upstream), auth: grok.NewAuth("", "grok-test", httptransport.Client(nil))}
	response, err := client.forwardExecution(t.Context(), t.Context(), grokTestRequest(t, false), grokTestHeaders(), "session")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || !bytes.Contains(body, []byte("GROK_OK")) {
		t.Fatalf("Grok HTTP response lost: %v", err)
	}
}

func TestProviderWebSocketAncillaryEventsAndEventOwnedCompletion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err = conn.Read(r.Context()); err != nil {
					return
				}
				for _, event := range []string{
					`{"type":"codex.response.metadata","headers":{"x-request-id":"per-response"}}`,
					`{"type":"codex.rate_limits","rate_limits":[]}`,
					`{"type":"responsesapi.websocket_timing","duration_ms":1}`,
					`{"type":"response.completed","response":{"id":"response","output":[]}}`,
				} {
					if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
						return
					}
				}
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.openai.EnableWebSockets(t.Context())
			defer client.openai.Close()
			parsed := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			output := httptest.NewRecorder()
			if err := executeRequest(t.Context(), t.Context(), parsed, codexAuthHeaders(), "session", client, output, nil, nil); err != nil {
				t.Fatal(err)
			}
			if stream {
				for _, kind := range []string{"codex.response.metadata", "codex.rate_limits", "responsesapi.websocket_timing"} {
					if !bytes.Contains(output.Body.Bytes(), []byte(kind)) {
						t.Fatalf("dropped ancillary event %s", kind)
					}
				}
			} else {
				var response struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(output.Body.Bytes(), &response); err != nil || response.Status != "completed" {
					t.Fatalf("nonstream event status missing: %s", output.Body.String())
				}
			}
		})
	}
	for _, kind := range []string{"unknown.event", "codex.unknown", "responsesapi.unknown"} {
		payload := mustTestJSON(t, map[string]string{"type": kind})
		if state := responseevents.ObserveTerminal(payload, true); state != responseevents.TerminalInvalid {
			t.Fatalf("unknown event %q became valid: %v", kind, state)
		}
	}
}

func TestWebSocketCaptureIncludesQueuedAndReservedReadsOnClose(t *testing.T) {
	first := []byte(`{"type":"response.created","response":{"status":"in_progress"}}`)
	queued := []byte(`{"type":"response.output_text.delta","delta":"already read"}`)
	reserved := []byte(`{"type":"response.completed","response":{"id":"old-response","status":"completed","output":[],"usage":{"input_tokens_details":{"cached_tokens":7}}}}`)
	var connections atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		if number == 1 {
			for _, payload := range [][]byte{first, queued, reserved} {
				if err := conn.Write(r.Context(), websocket.MessageText, payload); err != nil {
					return
				}
			}
		} else {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"new-response","status":"completed","output":[]}}`))
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	capture, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(t.Context())
	defer client.openai.Close()
	received := make(chan struct{})
	begin := client.openai.ObserveWebSocket
	client.openai.ObserveWebSocket = func(ctx context.Context, payload []byte, headers, responseHeaders http.Header) openai.Observation {
		return captureReceiveBarrier{Observation: begin(ctx, payload, headers, responseHeaders), terminal: reserved, received: received}
	}
	handler := capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, err := client.forwardExecution(r.Context(), r.Context(), []byte(`{"model":"model","stream":true,"input":[]}`), codexAuthHeaders(), "session")
		if err != nil {
			t.Error(err)
			return
		}
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			response.Body.Close()
			t.Error("receiver did not observe the queued terminal")
			return
		}
		_ = response.Body.Close()
		w.WriteHeader(http.StatusBadGateway)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":[]}`)))
	metrics := httptest.NewRecorder()
	capture.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	var snapshot struct {
		Transport struct {
			Response struct {
				Bytes uint64 `json:"bytes"`
			} `json:"provider_responses"`
		} `json:"transport"`
		Exchanges []struct {
			Attempts []struct {
				Evidence struct {
					CachedTokens *uint64 `json:"cached_tokens"`
				} `json:"provider_response"`
			} `json:"provider_attempts"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(metrics.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Transport.Response.Bytes != uint64(len(first)+len(queued)+len(reserved)) {
		t.Fatalf("already-read queued/reserved messages omitted or duplicated: %s", metrics.Body.String())
	}
	count := snapshot.Exchanges[0].Attempts[0].Evidence.CachedTokens
	if count == nil || *count != 7 {
		t.Fatal("already-received terminal evidence was discarded before capture Finish")
	}
	next, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"model","input":[]}`), codexAuthHeaders(), "session")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(next.Body)
	next.Body.Close()
	if err != nil || connections.Load() != 2 || !bytes.Contains(data, []byte("new-response")) || bytes.Contains(data, []byte("old-response")) {
		t.Fatalf("reserved old payload crossed into next request: connections=%d err=%v body=%s", connections.Load(), err, data)
	}
}

type captureReceiveBarrier struct {
	openai.Observation
	terminal []byte
	received chan struct{}
}

func (o captureReceiveBarrier) Message(payload []byte) {
	o.Observation.Message(payload)
	if bytes.Equal(payload, o.terminal) {
		close(o.received)
	}
}
