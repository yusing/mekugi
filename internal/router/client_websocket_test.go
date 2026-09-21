package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/capturer"
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
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	headers := http.Header{"Authorization": {"Bearer test"}, chatGPTAccountIDHeader: {"account"}}
	headers.Set(threadIDHeader, "thread")
	var previousLease *providerWebSocketLease
	for _, turn := range []string{"one", "two"} {
		headers.Set("x-codex-turn-state", turn)
		headers.Set(codexTurnMetadataHeader, turn)
		response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":true,"input":[]}`), headers, "session")
		if err != nil {
			t.Fatal(err)
		}
		current := response.Body.(*webSocketResponseBody)
		if previousLease != nil {
			// Force a delayed previous-lease callback after its connection has
			// been acquired by the next request. It must not close that request.
			current.entry.release(previousLease, false)
		}
		previousLease = current.lease
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
		if metadata["x-codex-turn-state"] != turn || metadata[codexTurnMetadataHeader] != turn {
			t.Fatalf("stale metadata: %v", metadata)
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("got %d connections, want one", connections.Load())
	}
}

func TestProviderWebSocketPoisonedLeasesAreNotReplayed(t *testing.T) {
	for _, scenario := range []string{"cancel", "close during read", "EOF", "invalid JSON", "oversized message", "idle timeout"} {
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
					case "oversized message":
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"`+strings.Repeat("x", 256)+`"}`))
					}
					return
				}
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.enableWebSockets(t.Context())
			defer client.websockets.close()
			if scenario == "idle timeout" {
				client.streamIdleTimeout = 20 * time.Millisecond
			}
			responseCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			response, err := client.forwardExecution(t.Context(), responseCtx, []byte(`{"model":"gpt-test","stream":true,"input":[]}`), codexAuthHeaders(), "session")
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			if scenario == "oversized message" {
				client.websockets.mu.Lock()
				for entry := range client.websockets.entries {
					entry.conn.SetReadLimit(128)
				}
				client.websockets.mu.Unlock()
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
				if _, err = io.ReadAll(response.Body); err == nil {
					t.Fatal("incomplete websocket stream succeeded")
				}
				if scenario == "idle timeout" {
					close(release)
					if !errors.Is(err, errUpstreamStreamIdleTimeout) {
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
			client.enableWebSockets(t.Context())
			defer client.websockets.close()
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
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
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
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":false,"input":[]}`), codexAuthHeaders(), "session")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	transform := &webSocketCountingTransform{}
	usage := tokenCounts{}
	state, err := copyUpstreamBodyTransformed(&output, response, false, transform, &responseHooks{onUsage: func(counts tokenCounts) { usage = counts }})
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
	if state != responseTerminalCompleted || len(terminal.Output) != 2 || terminal.Output[0].Type != "message" || terminal.Output[1].Type != "function_call" || transform.jsonCalls != 1 || transform.streamCalls != 0 || transform.finishes != 1 || usage.InputTokens != 5 {
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
	client := newProviderClient(upstream.URL, upstream.Client())
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	var responses []*http.Response
	for range providerWebSocketLimit {
		response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","stream":true,"input":[]}`), codexAuthHeaders(), "session")
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if response, err := client.forwardExecution(deadline, t.Context(), []byte(`{"model":"gpt-test","input":[]}`), codexAuthHeaders(), "session"); err == nil {
		response.Body.Close()
		t.Fatal("pool exceeded capacity")
	}
	if connections.Load() != providerWebSocketLimit {
		t.Fatal("capacity wait started another handshake")
	}
	client.websockets.close()
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
			client := newProviderClient(upstream.URL, upstream.Client())
			client.enableWebSockets(t.Context())
			defer client.websockets.close()
			call := func() {
				response, err := client.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-test","input":[]}`), codexAuthHeaders(), "session")
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
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
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

func TestProviderWebSocketMekugiTranslationAndCapture(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			item := map[string]any{"type": "custom_tool_call", "id": "item-H", "call_id": "call-H", "name": "shell", "input": testShellEditSource, "status": "completed"}
			terminal := map[string]any{"id": "response", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5}}
			payloads := [][]byte{
				mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}),
				mustTestJSON(t, map[string]any{"type": "response.completed", "response": terminal}),
			}
			sent := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_, request, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				sent <- request
				for _, payload := range payloads {
					if err := conn.Write(r.Context(), websocket.MessageText, payload); err != nil {
						return
					}
				}
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			capture, err := capturer.New(capturer.Config{Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			defer capture.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.httpClient.Transport = capture.Transport(client.httpClient.Transport)
			client.enableWebSockets(t.Context())
			defer client.websockets.close()
			calls := 0
			proxy := newManagedMekugiProxy(t)
			workspace := t.TempDir()
			parsed := serverRequest(t, func(fields map[string]any) {
				fields["stream"] = stream
				fields["input"] = []any{map[string]any{"role": "user", "content": "task"}}
				fields["tools"] = testNativeResponsesTools()
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(parsed.originalBody))
			request.Header = serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
			maps.Copy(request.Header, codexAuthHeaders())
			request.Header.Set(sessionIDHeader, "session")
			handler := capture.Handler(responsesHandler(t.Context(), time.Minute, client, nil, proxy, nil))
			output := httptest.NewRecorder()
			handler.ServeHTTP(output, request)
			if output.Code != 200 || calls != 0 || !strings.Contains(output.Body.String(), nativeExecCommandToolName) || strings.Contains(output.Body.String(), `"name":"hpatch"`) {
				t.Fatalf("translation changed: status=%d calls=%d body=%s", output.Code, calls, output.Body.String())
			}
			providerRequest := <-sent
			if bytes.Contains(providerRequest, []byte(`"name":"apply_patch"`)) || !bytes.Contains(providerRequest, []byte(`"name":"shell"`)) {
				t.Fatal("tool request projection bypassed on WS")
			}
			metrics := httptest.NewRecorder()
			capture.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
			var snapshot struct {
				Requests struct {
					Logical, Completed uint64
					Attempts           uint64 `json:"provider_attempts"`
				} `json:"requests"`
				Transport struct {
					Request struct {
						Bytes uint64 `json:"bytes"`
					} `json:"provider_attempt_requests"`
					Response struct {
						Bytes uint64 `json:"bytes"`
					} `json:"provider_responses"`
				} `json:"transport"`
				Usage struct {
					Input  uint64 `json:"input_tokens"`
					Output uint64 `json:"output_tokens"`
				} `json:"usage"`
				Capture struct {
					Errors     uint64 `json:"capture_errors"`
					Incomplete uint64 `json:"incomplete_records"`
					Missing    uint64 `json:"missing_provider_records"`
				} `json:"capture"`
			}
			if err := json.Unmarshal(metrics.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Requests.Logical != 1 || snapshot.Requests.Completed != 1 || snapshot.Requests.Attempts != 1 || snapshot.Transport.Request.Bytes != uint64(len(providerRequest)) || snapshot.Transport.Response.Bytes != uint64(len(payloads[0])+len(payloads[1])) || snapshot.Usage.Input != 20 || snapshot.Usage.Output != 5 || snapshot.Capture.Errors != 0 || snapshot.Capture.Incomplete != 0 || snapshot.Capture.Missing != 0 {
				t.Fatalf("WS router/capture contract: %s", metrics.Body.String())
			}
		})
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
		done <- RunSession(ctx, []string{"--mode", "passthrough"}, nil, func(session Session) { ready <- session.BaseURL }, nil)
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
		request.Header.Set(threadIDHeader, "thread")
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
	headers.Set(codexTurnMetadataHeader, "bounded header metadata")
	headers.Set("x-codex-turn-state", "current state")
	payload, handshake, _, err := webSocketRequest([]byte(`{"model":"model","input":[],"client_metadata":{"x-codex-turn-metadata":"full body metadata","custom":"preserved"}}`), headers, "session")
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Metadata map[string]string `json:"client_metadata"`
	}
	if err := json.Unmarshal(payload, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Metadata[codexTurnMetadataHeader] != "full body metadata" || sent.Metadata["custom"] != "preserved" || sent.Metadata["x-codex-turn-state"] != "current state" || handshake.Get(codexTurnMetadataHeader) != "" || handshake.Get("x-codex-turn-state") != "" {
		t.Fatal("body metadata overwritten or per-turn metadata retained in handshake")
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
	capture, err := capturer.New(capturer.Config{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
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
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	client.grok = &grokClient{httpClient: grokTestHTTPClient(t, upstream), auth: newGrokAuth("", "grok-test")}
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
			client.enableWebSockets(t.Context())
			defer client.websockets.close()
			parsed := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			output := httptest.NewRecorder()
			if err := executeRequest(t.Context(), t.Context(), parsed, codexAuthHeaders(), "session", client, output, nil, nil, nil); err != nil {
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
		if state := observeResponseTerminal(payload, true); state != responseTerminalInvalid {
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
	capture, err := capturer.New(capturer.Config{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	handler := capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, err := client.forwardExecution(r.Context(), r.Context(), []byte(`{"model":"model","stream":true,"input":[]}`), codexAuthHeaders(), "session")
		if err != nil {
			t.Error(err)
			return
		}
		body := response.Body.(*webSocketResponseBody)
		// The bounded queue holds the second message; the receiver has read
		// and observed the third but is blocked delivering it. This is the
		// release/admission boundary that previously lost bytes and ownership.
		deadline := time.Now().Add(5 * time.Second)
		for {
			client.websockets.mu.Lock()
			pending := body.lease.pending
			terminal := body.lease.terminal
			client.websockets.mu.Unlock()
			if pending && terminal {
				break
			}
			if time.Now().After(deadline) {
				response.Body.Close()
				t.Error("receiver never reserved its blocked delivery")
				return
			}
			time.Sleep(time.Millisecond)
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
