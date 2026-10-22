package requestexecution

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

type compactProvider func([]byte, http.Header) *http.Response

func (provider compactProvider) ForwardExecution(_, _ context.Context, body []byte, headers http.Header, _ string) (*inferencegateway.Response, error) {
	return &inferencegateway.Response{Response: provider(body, headers), Transport: "http"}, nil
}

func TestCompactionProviderTurnSeparatesSummaryAndMetadata(t *testing.T) {
	for _, wireKind := range []string{"json", "stream-full", "stream-items"} {
		stream := wireKind != "json"
		for _, status := range []string{"completed", "failed", "incomplete"} {
			t.Run(status+"/"+wireKind, func(t *testing.T) {
				request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-6-astra","stream":true,"input":[{"role":"user","content":"Original task."},{"type":"compaction_trigger"},{"type":"additional_tools","tools":[]}],"tools":[],"tool_choice":"auto","parallel_tool_calls":true}`))
				if err != nil {
					t.Fatal(err)
				}
				headers := http.Header{}
				headers.Set(codexresponses.ThreadIDHeader, "thread")
				headers.Set(codexresponses.TurnMetadataHeader, `{"thread_id":"thread","turn_id":"compact-turn","request_kind":"compaction","compaction":{"implementation":"responses_compaction_v2"},"extension":"\u754c"}`)
				request.Fields["client_metadata"] = responses.MustMarshalJSON(map[string]string{codexresponses.TurnMetadataHeader: headers.Get(codexresponses.TurnMetadataHeader)})
				const summary = "The model's summary, unchanged.\n"
				const metadata = `{"sources":[{"path":"selected.go"}]}`
				calls, selections := 0, 0
				provider := compactProvider(func(body []byte, forwarded http.Header) *http.Response {
					calls++
					turn, ok := codexresponses.DecodeTurnMetadata(forwarded)
					if !ok || turn.RequestKind != responses.Turn || bytes.Contains(body, []byte("compaction_trigger")) || bytes.Contains(body, []byte("additional_tools")) || !bytes.Contains(body, []byte(`"tools":[]`)) || !bytes.Contains(body, []byte(`"parallel_tool_calls":false`)) || !bytes.Contains(body, []byte("Original prompt plus ours.")) || !bytes.Contains(body, []byte("Original task.")) {
						t.Fatalf("not an ordinary summary-generation turn: %s %+v", body, turn)
					}
					var sent struct {
						ClientMetadata map[string]string `json:"client_metadata"`
					}
					if err := json.Unmarshal(body, &sent); err != nil {
						t.Fatal(err)
					}
					if sent.ClientMetadata[codexresponses.TurnMetadataHeader] != forwarded.Get(codexresponses.TurnMetadataHeader) || !strings.Contains(forwarded.Get(codexresponses.TurnMetadataHeader), `"extension":"\u754c"`) {
						t.Fatalf("client metadata or extension changed: %s", body)
					}
					item := map[string]any{"type": "message", "id": "model-summary", "role": "assistant", "status": "completed", "content": []map[string]string{{"type": "output_text", "text": summary + "<mekugi-reload-v1>" + metadata + "</mekugi-reload-v1>"}}}
					response := map[string]any{"id": "provider-response", "status": status, "output": []any{item}, "usage": map[string]int{"output_tokens": 42}}
					if status == "failed" {
						response["error"] = map[string]string{"code": "fixture_failure", "message": "provider failure detail"}
						response["output"] = []any{}
					}
					var wire bytes.Buffer
					contentType := "application/json"
					if stream {
						contentType = "text/event-stream"
						// Comments and item-done output are valid without output on the terminal.
						wire.WriteString(": keepalive\n\n")
						if status == "completed" {
							_ = codexresponses.WriteEvent(&wire, responses.MustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}))
							if wireKind == "stream-items" {
								response["output"] = []any{}
							}
						}
						_ = codexresponses.WriteEvent(&wire, responses.MustMarshalJSON(map[string]any{"type": "response." + status, "response": response}))
					} else {
						wire.Write(responses.MustMarshalJSON(response))
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(&wire)}
				})
				executor := Executor{Provider: provider, CompactPrompt: func() string { return "Original prompt plus ours." }, Issues: testCriticalErrors()}
				executor.CompactStore = func(_ context.Context, thread, text, reload string) (string, error) {
					selections++
					if text != summary || reload != metadata || thread != "thread" {
						t.Fatal("summary or snapshots changed")
					}
					return "mekugi:compact:thread:amber", nil
				}
				output := httptest.NewRecorder()
				err = executor.ExecuteHTTP(t.Context(), t.Context(), request, headers, output)
				if calls != 1 {
					t.Fatalf("provider turns=%d", calls)
				}
				if strings.Contains(output.Body.String(), "mekugi-reload") || strings.Contains(output.Body.String(), "selected.go") {
					t.Fatalf("reload metadata reached Codex: %s", output.Body)
				}
				if status != "completed" {
					if selections != 0 || !strings.Contains(output.Body.String(), `"type":"response.`+status+`"`) {
						t.Fatalf("failed turn admitted metadata or lost terminal: %s (%v)", output.Body, err)
					}
					return
				}
				if err != nil || selections != 1 {
					t.Fatalf("summary delivery: %v selections=%d", err, selections)
				}
				if len(executor.Issues.Notices.Pending()) != 0 {
					t.Fatal("successful summary raised a failure notice")
				}
				if !strings.Contains(output.Body.String(), `"encrypted_content":"mekugi:compact:thread:amber"`) || strings.Contains(output.Body.String(), summary) {
					t.Fatalf("missing retained reference: %s", output.Body)
				}

			})
		}
	}
}

func TestCompactionCollectionDoesNotConfirmHostDelivery(t *testing.T) {
	hooks := new(ResponseHooks)
	collector := &compactSummary{ResponseHooks: hooks}
	collector.Delivered(jsontext.Value(`{"type":"response.completed","response":{"id":"r","status":"completed"}}`))
	if hooks.deliveredTerminal {
		t.Fatal("buffered provider output marked delivered to Codex")
	}
}
