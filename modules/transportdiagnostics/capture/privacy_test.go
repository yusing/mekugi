package capture

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCaptureMetadataPrivacy(t *testing.T) {
	for _, value := range []string{"valid-model_1:tool/name", "private prompt text", "https://private.example/token", strings.Repeat("x", 257)} {
		for _, websocket := range []bool{false, true} {
			t.Run(fmt.Sprintf("length%d/websocket%t", len(value), websocket), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "capture.jsonl")
				recorder, err := new(Service).Open(Config{Output: path, Mode: "mekugi"})
				if err != nil {
					t.Fatal(err)
				}
				defer recorder.Close()
				payload := fmt.Sprintf(`{"model":%q,"tools":[{"name":%q},{"function":{"name":%q}},{"name":"stock_tool"}]}`, value, value, value)
				response := fmt.Sprintf(`{"status":"completed","model":%q,"service_tier":%q,"output":[]}`, value, value)
				providerHeaders := http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {value}, "Openai-Model": {value}}
				headers := http.Header{}
				for _, key := range []string{"x-client-request-id", "session-id", "Session_id", "thread-id", "x-openai-subagent"} {
					headers.Set(key, value)
				}
				checkForwarded := func(request *http.Request) {
					t.Helper()
					for key := range headers {
						if request.Header.Get(key) != value {
							t.Fatalf("capture changed forwarded header %s", key)
						}
					}
					body, err := io.ReadAll(request.Body)
					if err != nil || string(body) != payload {
						t.Fatalf("capture changed forwarded body: %q, %v", body, err)
					}
				}
				handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					checkForwarded(request)
					ctx := request.Context()
					if websocket {
						var exchange *ResponsesWebSocket
						ctx, exchange = BeginResponsesWebSocket(ctx, request.Header, []byte(payload))
						ObserveProjectedRequest(ctx, []byte(payload))
						attempt := BeginWebSocketAttempt(ctx, []byte(payload), request.Header, providerHeaders)
						attempt.Message([]byte(response))
						attempt.Finish(nil)
						exchange.Message([]byte(response))
						exchange.Finish(nil)
						for _, boundary := range []ResponsesWebSocketControlBoundary{ResponsesWebSocketControlCodex, ResponsesWebSocketControlProvider} {
							ObserveResponsesWebSocketControl(ctx, boundary, ResponsesWebSocketControlRequest, []byte(payload))
						}
					} else {
						ObserveProjectedRequest(ctx, []byte(payload))
						transport := recorder.Transport(roundTripperFunc(func(outgoing *http.Request) (*http.Response, error) {
							checkForwarded(outgoing)
							return &http.Response{StatusCode: http.StatusOK, Header: providerHeaders, Body: io.NopCloser(strings.NewReader(response))}, nil
						}))
						outgoing := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(payload)).WithContext(ctx)
						outgoing.Header = request.Header.Clone()
						result, err := transport.RoundTrip(outgoing)
						if err != nil {
							t.Fatal(err)
						}
						_, _ = io.Copy(io.Discard, result.Body)
						_ = result.Body.Close()
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}))
				method := http.MethodPost
				if websocket {
					method = http.MethodGet
				}
				request := httptest.NewRequest(method, "/v1/responses", strings.NewReader(payload))
				request.Header = headers
				visible := httptest.NewRecorder()
				handler.ServeHTTP(visible, request)
				if visible.Body.String() != response {
					t.Fatal("capture changed delivered response")
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				if value == "valid-model_1:tool/name" {
					want = value
				}
				decoder := jsontext.NewDecoder(bytes.NewReader(raw))
				count := 0
				for {
					var record captureRecord
					if err := json.UnmarshalDecode(decoder, &record); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					count++
					if record.RequestID != want || record.SessionID != want || record.ThreadID != want || record.Subagent != want {
						t.Fatalf("retained request identity = %+v", record)
					}
					if record.Boundary == "provider" {
						evidence := record.ProviderResponse
						if evidence == nil || evidence.Model != want || evidence.HeaderModel != want || evidence.RequestID != want || evidence.ServiceTier != want {
							t.Fatalf("retained provider identity = %+v", evidence)
						}
					}
					if record.Boundary == "codex" || record.Boundary == "provider" {
						tools := []string{"stock_tool"}
						if want != "" {
							tools = append(tools, want)
						}
						if record.RequestModel != want || !slices.Equal(record.RequestTools, tools) || record.Request.Bytes != uint64(len(payload)) {
							t.Fatalf("retained request metadata = %+v", record)
						}
					}
				}
				wantRecords := 2
				if websocket {
					wantRecords = 4
				}
				if count != wantRecords {
					t.Fatalf("records = %d, want %d", count, wantRecords)
				}
				snapshot := recorder.Snapshot()
				encoded, err := json.Marshal(&snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if want == "" && (bytes.Contains(raw, []byte(value)) || bytes.Contains(encoded, []byte(value))) {
					t.Fatal("capture or metrics retained private metadata")
				}
			})
		}
	}
}

func TestCaptureResponseStatusPrivacy(t *testing.T) {
	for _, status := range []string{"private response text", "unexpected_status", "completed", "in_progress"} {
		var record captureRecord
		observeResponseJSON([]byte(fmt.Sprintf(`{"status":%q,"output":[]}`, status)), &record)
		want := ""
		if status == "completed" || status == "in_progress" {
			want = status
		}
		if record.ResponseStatus != want {
			t.Errorf("status %q retained as %q, want %q", status, record.ResponseStatus, want)
		}
	}
}
