package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func TestCaptureModelOutputThroughRouterCommentary(t *testing.T) {
	for _, format := range []string{"JSON", "SSE"} {
		t.Run(format, func(t *testing.T) {
			transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
			message := map[string]any{"type": "message", "id": "msg-model", "role": "assistant", "status": "completed", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "Tokens: this text was authored by the model"}}}
			tool := map[string]any{"type": "function_call", "id": "fc-model", "call_id": "call-model", "name": "lookup", "arguments": `{"key":"value"}`, "status": "completed"}
			terminal := map[string]any{"id": "resp-capture", "status": "completed", "output": []any{message, tool}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5}}
			payload := mustTestJSON(t, terminal)
			contentType := "application/json"
			if format == "SSE" {
				contentType = "text/event-stream"
				terminal["output"] = []any{}
				var stream bytes.Buffer
				for index, item := range []any{message, tool} {
					stream.WriteString("data: ")
					stream.Write(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": index, "item": item}))
					stream.WriteString("\n\n")
				}
				stream.WriteString("data: ")
				stream.Write(mustTestJSON(t, map[string]any{"type": "response.completed", "response": terminal}))
				stream.WriteString("\n\n")
				payload = stream.Bytes()
			}
			recorder, err := capturer.New(capturer.Config{Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = recorder.Close() })
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write(payload)
			}))
			t.Cleanup(upstream.Close)
			client := &http.Client{Transport: recorder.Transport(http.DefaultTransport)}
			handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requestBody, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				providerRequest, err := http.NewRequestWithContext(request.Context(), http.MethodPost, upstream.URL+"/responses", bytes.NewReader(requestBody))
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(providerRequest)
				if err != nil {
					t.Fatal(err)
				}
				capturer.ObserveProviderUsage(request.Context(), capturer.ProviderUsage{InputTokens: 20, OutputTokens: 5})
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", contentType)
				if format == "JSON" {
					observeTestResponseUsage(t, transform, body, false)
					transformed, err := transform.TransformJSON(body)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = w.Write(transformed)
					return
				}
				for raw := range bytes.SplitSeq(bytes.TrimSpace(body), []byte("\n\n")) {
					event := bytes.TrimPrefix(raw, []byte("data: "))
					var envelope struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal(event, &envelope); err != nil {
						t.Fatal(err)
					}
					if envelope.Type == "response.completed" {
						observeTestResponseUsage(t, transform, event, true)
					}
					events, err := transform.TransformSSE(event)
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						_, _ = w.Write(append(append([]byte("data: "), event...), '\n', '\n'))
					}
				}
			}))
			front := httptest.NewRecorder()
			handler.ServeHTTP(front, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`)))
			if strings.Contains(front.Body.String(), "| Input | 20 |") || !strings.Contains(front.Body.String(), "Tokens: this text was authored") {
				t.Fatalf("commentary delivery changed: %s", front.Body.String())
			}
			metrics := httptest.NewRecorder()
			recorder.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
			var snapshot struct {
				Semantic struct {
					Client struct {
						Bytes int `json:"bytes"`
					} `json:"client_outputs"`
				} `json:"semantic"`
				Usage struct {
					Output int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(metrics.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Semantic.Client.Bytes < 100 || snapshot.Usage.Output != 5 {
				t.Fatalf("router commentary affected model output: %s", metrics.Body.String())
			}
		})
	}
}
