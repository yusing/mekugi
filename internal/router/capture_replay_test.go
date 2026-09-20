package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

type captureReplayProvider struct {
	client *http.Client
	url    string
}

func (p captureReplayProvider) forwardExecution(ctx, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return p.client.Do(request)
}

func TestCaptureRequestBaselineAfterMekugiReplay(t *testing.T) {
	t.Parallel()
	const catWrite = "foo; cat > cache-replay.txt <<'EOF'\nliteral content\nEOF\nbar"
	for _, fixture := range []struct{ name, input, protocol string }{
		{"hpatch", testMekugiScript, "native"},
		{"hpatch", testMekugiScript, "ctp2"},
		{"shell", catWrite, "native"},
		{"shell", catWrite, "ctp2"},
	} {
		protocol := fixture.protocol
		t.Run(fixture.name+"/"+protocol, func(t *testing.T) {
			recorder, err := capturer.New(capturer.Config{Mode: "mekugi", ModelProtocol: protocol})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = recorder.Close() })
			var forwarded [][]byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				forwarded = append(forwarded, body)
				w.Header().Set("Content-Type", "application/json")
				output := []any{testMekugiItem()}
				output[0].(map[string]any)["name"] = fixture.name
				output[0].(map[string]any)["input"] = fixture.input
				if len(forwarded) > 1 {
					output = []any{journalFinishCall(`{"op":"finish"}`)}
				}
				_, _ = w.Write(mustTestJSON(t, map[string]any{"status": "completed", "output": output}))
			}))
			t.Cleanup(upstream.Close)
			provider := captureReplayProvider{client: &http.Client{Transport: recorder.Transport(http.DefaultTransport)}, url: upstream.URL}
			proxy := newManagedMekugiProxy(t)
			var codec *ctp2Codec
			if protocol == "ctp2" {
				codec = mustCTP2Codec(t)
			}
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
			handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				parsed, err := parseResponsesRequest(body)
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := executeRequest(r.Context(), r.Context(), parsed, headers, "capture-replay", provider, w, nil, proxy, codec, nil); err != nil {
					t.Error(err)
				}
			}))
			initial := serverRequest(t, func(fields map[string]any) { fields["instructions"] = stockModelInstructionsForTest("", "") })
			first := httptest.NewRecorder()
			firstRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(initial.originalBody))
			firstRequest.Header = headers.Clone()
			handler.ServeHTTP(first, firstRequest)
			var response struct {
				Output []json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var carrier json.RawMessage
			for _, item := range response.Output {
				var obj map[string]json.RawMessage
				_ = json.Unmarshal(item, &obj)
				if jsonString(obj, "type") == "custom_tool_call" {
					carrier = item
				}
			}
			if carrier == nil {
				t.Fatal("missing delivered carrier")
			}
			if fixture.name == "shell" && (!bytes.Contains(carrier, []byte("tools.exec_command(")) || bytes.Contains(carrier, []byte("tools.apply_patch("))) {
				t.Fatalf("shell write did not retain its execution carrier: %s", carrier)
			}
			next := serverRequest(t, func(fields map[string]any) {
				fields["instructions"] = stockModelInstructionsForTest("", "")
				fields["input"] = append(fields["input"].([]any), carrier, map[string]any{"type": "custom_tool_call_output", "call_id": "call-H", "output": strings.Repeat("repeated result text with enough exact words; ", 24)})
			})
			second := httptest.NewRecorder()
			secondRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(next.originalBody))
			secondRequest.Header = headers.Clone()
			handler.ServeHTTP(second, secondRequest)
			if len(forwarded) != 2 {
				t.Fatalf("provider requests %d", len(forwarded))
			}
			var envelope struct {
				Input []map[string]json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(forwarded[1], &envelope)
			restored := false
			for _, item := range envelope.Input {
				if jsonString(item, "name") == fixture.name {
					restored = true
				}
			}
			if !restored {
				t.Fatal("provider history did not restore original tool call")
			}
			metrics := httptest.NewRecorder()
			recorder.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
			var snapshot struct {
				Protocol struct {
					Input int64 `json:"input_payload_tokens_saved"`
				} `json:"protocol"`
				Capture struct {
					Errors int `json:"capture_errors"`
				} `json:"capture"`
				Exchanges []struct {
					Diagnosis struct {
						Native struct {
							Status string `json:"status"`
						} `json:"native"`
						Provider struct {
							Status string `json:"status"`
						} `json:"provider"`
					} `json:"cache_diagnostics"`
					Client struct {
						Tokens int64 `json:"tokens"`
					} `json:"client_request"`
					Attempts []struct {
						Native struct {
							Tokens int64 `json:"tokens"`
						} `json:"native_request"`
						Request struct {
							Tokens int64 `json:"tokens"`
						} `json:"request"`
					} `json:"provider_attempts"`
				} `json:"exchanges"`
			}
			if err := json.Unmarshal(metrics.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Exchanges) != 2 || snapshot.Exchanges[1].Diagnosis.Native.Status != "appended" || snapshot.Exchanges[1].Diagnosis.Provider.Status != "appended" {
				t.Fatalf("replay/CTP changed an existing prefix: %+v", snapshot.Exchanges)
			}
			var expected int64
			var differs bool
			for _, exchange := range snapshot.Exchanges {
				for _, attempt := range exchange.Attempts {
					expected += attempt.Native.Tokens - attempt.Request.Tokens
					differs = differs || exchange.Client.Tokens != attempt.Native.Tokens
				}
			}
			if !differs || snapshot.Protocol.Input != expected || snapshot.Capture.Errors != 0 {
				t.Fatalf("replay baseline = %+v, expected %d", snapshot, expected)
			}
			if protocol == "native" && expected != 0 {
				t.Fatalf("replay incorrectly counted as compression: %d", expected)
			}
		})
	}
}
