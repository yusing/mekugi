package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/capturer"
)

func TestResponsesWebSocketCaptureSeparatesSteeringAndAutomaticRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	directory := t.TempDir()
	record, err := capturer.New(capturer.Config{Output: filepath.Join(directory, "capture.jsonl"), Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer record.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		if _, err := providerSocketRead(ctx, conn); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, conn, socketEvent("response.created", "parent")); err != nil {
			t.Error(err)
			return
		}
		if _, err := providerSocketRead(ctx, conn); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, conn, map[string]any{"type": "response.steer.accepted", "steer": map[string]string{"id": "accepted", "previous_response_id": "parent"}}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, conn, map[string]any{"type": "response.incomplete", "response": map[string]any{
			"id": "parent", "status": "incomplete", "incomplete_details": map[string]string{"reason": "steered"}, "output": []any{},
		}}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, conn, socketEvent("response.created", "successor")); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, conn, socketEvent("response.completed", "successor")); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer provider.Close()
	handler := responsesWebSocketHandler(ctx, 5*time.Second, newProviderClient(provider.URL, provider.Client()), nil, nil, nil)
	defer handler.Close()
	finished := make(chan struct{})
	server := httptest.NewServer(record.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		handler.ServeHTTP(w, r)
	})))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	create := map[string]any{"type": "response.create", "model": "gpt-test", "input": "task"}
	steer := map[string]any{"type": "response.steer", "previous_response_id": "parent", "input": "new direction"}
	socketWrite(t, ctx, conn, create)
	_ = socketRead(t, ctx, conn)
	socketWrite(t, ctx, conn, steer)
	for range 4 {
		if _, err := providerSocketRead(ctx, conn); err != nil {
			t.Error(err)
			return
		}
	}
	conn.CloseNow()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("router did not finish after disconnect")
	}
	response := httptest.NewRecorder()
	record.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	var snapshot struct {
		Capture struct {
			CaptureErrors int `json:"capture_errors"`
		} `json:"capture"`
		Requests struct {
			Logical  int `json:"logical"`
			Attempts int `json:"provider_attempts"`
		} `json:"requests"`
		Transport map[string]struct {
			Bytes int `json:"bytes"`
		} `json:"transport"`
		Exchanges []struct {
			Request struct {
				Bytes int `json:"bytes"`
			} `json:"client_request"`
			Attempts []struct {
				ProjectedRequest *struct {
					Bytes int `json:"bytes"`
				} `json:"projected_request"`
				Request struct {
					Bytes int `json:"bytes"`
				} `json:"request"`
			} `json:"provider_attempts"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Requests.Logical != 2 || snapshot.Requests.Attempts != 2 {
		t.Fatalf("logical exchanges = %+v", snapshot.Requests)
	}
	if snapshot.Transport["client_requests"].Bytes != len(mustMarshalJSON(create)) ||
		snapshot.Transport["client_control_requests"].Bytes != len(mustMarshalJSON(steer)) ||
		snapshot.Transport["provider_control_requests"].Bytes != len(mustMarshalJSON(steer)) ||
		snapshot.Transport["provider_control_responses"].Bytes == 0 ||
		snapshot.Transport["client_control_responses"].Bytes == 0 {
		t.Fatalf("transport attribution = %+v", snapshot.Transport)
	}
	if snapshot.Capture.CaptureErrors != 0 {
		t.Fatalf("capture errors = %d", snapshot.Capture.CaptureErrors)
	}
	var automatic bool
	for _, exchange := range snapshot.Exchanges {
		if exchange.Request.Bytes == 0 && len(exchange.Attempts) == 1 {
			automatic = true
			if exchange.Attempts[0].ProjectedRequest == nil || exchange.Attempts[0].ProjectedRequest.Bytes != 0 {
				t.Fatal("missing zero-byte projected request for automatic successor")
			}
			if exchange.Attempts[0].Request.Bytes != 0 {
				t.Fatal("fabricated provider request for automatic successor")
			}
		}
	}
	if !automatic {
		t.Fatal("missing zero-request automatic successor")
	}
}

func TestResponsesWebSocketPreservesUpgradeRejectionStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "10000") // Deliberately truncated error body.
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","code":"invalid_api_key","message":"refresh authentication"}}`))
	}), nil, codexAuthHeaders())
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "task"})
	event := socketRead(t, ctx, conn)
	if string(event["status"]) != "401" || jsonString(event, "type") != "error" {
		t.Fatalf("upgrade status lost: %s", mustMarshalJSON(event))
	}
	var detail map[string]json.RawMessage
	_ = json.Unmarshal(event["error"], &detail)
	if jsonString(detail, "code") != "invalid_api_key" {
		t.Fatalf("provider error lost: %s", event["error"])
	}
}
