package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
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

func TestProviderWebSocketAncillaryResponseStatus(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, rejected := range []bool{false, true} {
			t.Run("stream="+strconv.FormatBool(stream)+"/error="+strconv.FormatBool(rejected), func(t *testing.T) {
				messages := [][]byte{
					[]byte(`{"type":"codex.response.metadata","headers":{"x-request-id":"current-response"}}`),
					[]byte(`{"type":"codex.rate_limits","rate_limits":[]}`),
					[]byte(`{"type":"responsesapi.websocket_timing","duration_ms":1}`),
				}
				last := []byte(`{"type":"response.completed","response":{"id":"response","status":"completed","output":[]}}`)
				if rejected {
					last = []byte(`{"type":"error","status":429,"headers":{"Retry-After":"7","x-request-id":"error-response"},"error":{"message":"rate limited"}}`)
				}
				messages = append(messages, last)
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
					for _, message := range messages {
						if err := conn.Write(r.Context(), websocket.MessageText, message); err != nil {
							return
						}
					}
					_, _, _ = conn.Read(r.Context())
				}))
				defer upstream.Close()
				recorder, err := capturer.New(capturer.Config{Mode: "passthrough", ModelProtocol: "native"})
				if err != nil {
					t.Fatal(err)
				}
				defer recorder.Close()
				client := newProviderClient(upstream.URL, upstream.Client())
				client.enableWebSockets(t.Context())
				defer client.websockets.close()
				handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					response, err := client.forwardExecution(r.Context(), r.Context(), []byte(`{"model":"model","stream":`+strconv.FormatBool(stream)+`,"input":[]}`), codexAuthHeaders(), "session")
					if err != nil {
						t.Error(err)
						w.WriteHeader(502)
						return
					}
					defer response.Body.Close()
					maps.Copy(w.Header(), response.Header)
					w.WriteHeader(response.StatusCode)
					if _, err := io.Copy(w, response.Body); err != nil {
						t.Error(err)
					}
				}))
				front := httptest.NewRecorder()
				handler.ServeHTTP(front, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":[]}`)))
				if sends.Load() != 1 {
					t.Fatalf("unexpected inference replay: %d", sends.Load())
				}
				if rejected {
					if front.Code != 429 || front.Header().Get("Retry-After") != "7" || front.Header().Get("x-request-id") != "error-response" || front.Header().Get("Content-Type") != "application/json" || !bytes.Equal(front.Body.Bytes(), last) {
						t.Fatalf("structured error lost after ancillary prefix: status=%d headers=%v body=%s", front.Code, front.Header(), front.Body.String())
					}
				} else if stream {
					var expected bytes.Buffer
					for _, message := range messages {
						expected.WriteString("data: ")
						expected.Write(message)
						expected.WriteString("\n\n")
					}
					if front.Code != 200 || !bytes.Equal(front.Body.Bytes(), expected.Bytes()) {
						t.Fatalf("successful SSE prefix order changed: %s", front.Body.String())
					}
				} else if front.Code != 200 || !json.Valid(front.Body.Bytes()) || bytes.Contains(front.Body.Bytes(), []byte("codex.response.metadata")) {
					t.Fatalf("nonstream success corrupted: %s", front.Body.String())
				}
				metrics := httptest.NewRecorder()
				recorder.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
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
				var expectedBytes uint64
				for _, message := range messages {
					expectedBytes += uint64(len(message))
				}
				if snapshot.Transport.Response.Bytes != expectedBytes {
					t.Fatalf("prefix capture omitted/duplicated messages: got %d want %d", snapshot.Transport.Response.Bytes, expectedBytes)
				}
			})
		}
	}
}

func TestProviderWebSocketAncillaryStartDeadline(t *testing.T) {
	t.Parallel()
	var messages atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		for {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"codex.rate_limits","rate_limits":[]}`)); err != nil {
				return
			}
			messages.Add(1)
			time.Sleep(time.Millisecond)
		}
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	responseCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	startCtx, cancelStart := context.WithTimeout(responseCtx, 200*time.Millisecond)
	defer cancelStart()
	response, err := client.forwardExecution(startCtx, responseCtx, []byte(`{"model":"model","stream":true,"input":[]}`), codexAuthHeaders(), "session")
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("ancillary event prematurely ended the response-start timeout")
	}
	if startCtx.Err() == nil || responseCtx.Err() != nil || messages.Load() < 2 {
		t.Fatalf("startup was not bounded by its original deadline during ancillary activity: start=%v response=%v messages=%d", startCtx.Err(), responseCtx.Err(), messages.Load())
	}
}

func TestProviderWebSocketAncillaryPrefixBudget(t *testing.T) {
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
		payload := []byte(`{"type":"codex.response.metadata","padding":"` + strings.Repeat("x", maxUpstreamSniffBytes) + `"}`)
		_ = conn.Write(r.Context(), websocket.MessageText, payload)
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.enableWebSockets(t.Context())
	defer client.websockets.close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	response, err := client.forwardExecution(ctx, ctx, []byte(`{"model":"model","stream":true,"input":[]}`), codexAuthHeaders(), "session")
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "inspection budget") || sends.Load() != 1 {
		t.Fatalf("unbounded ancillary prefix or replay: err=%v sends=%d", err, sends.Load())
	}
}
