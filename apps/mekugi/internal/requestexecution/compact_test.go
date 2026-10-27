package requestexecution

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
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
						response["error"] = map[string]string{"code": "server_is_overloaded", "message": "provider failure detail"}
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
					if status == "failed" && (!strings.Contains(output.Body.String(), `"code":"server_is_overloaded"`) || !strings.Contains(output.Body.String(), "provider failure detail")) {
						t.Fatalf("compaction bridge lost failure classification/evidence: %s", output.Body)
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

func TestCompactionStreamPublishesProgressBeforeSummary(t *testing.T) {
	for _, outcome := range []string{"completed", "storage failure", "canceled", "disconnected"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				reader, upstream := io.Pipe()
				defer upstream.Close()
				request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-6-astra","stream":true,"input":[],"tools":[]}`))
				if err != nil {
					t.Fatal(err)
				}
				headers := http.Header{}
				headers.Set(codexresponses.ThreadIDHeader, "thread")
				headers.Set(codexresponses.TurnMetadataHeader, `{"thread_id":"thread","turn_id":"compact-turn","request_kind":"compaction","compaction":{"implementation":"responses_compaction_v2"}}`)
				stored := false
				storeFailure := errors.New("storage unavailable")
				executor := Executor{
					Provider: compactProvider(func([]byte, http.Header) *http.Response {
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
					}),
					CompactPrompt: func() string { return "Summarize." },
					CompactStore: func(context.Context, string, string, string) (string, error) {
						stored = true
						if outcome == "storage failure" {
							return "", storeFailure
						}
						return "retained-summary", nil
					},
				}
				output := httptest.NewRecorder()
				var destination http.ResponseWriter = output
				disconnected := &compactDisconnectedOutput{ResponseRecorder: output}
				if outcome == "disconnected" {
					destination = disconnected
				}
				done := make(chan error, 1)
				go func() { done <- executor.ExecuteHTTP(ctx, ctx, request, headers, destination) }()
				synctest.Wait()
				if !output.Flushed || output.Header().Get("Content-Type") != "text/event-stream" {
					t.Fatal("compaction headers withheld while waiting for upstream")
				}
				for index, frame := range []string{
					`data: {"type":"response.created","response":{"id":"summary-response","output":[{"text":"PRIVATE"}]}}` + "\n\n",
					`data: {"type":"response.reasoning_text.delta","delta":"PRIVATE"}` + "\n\n",
					": PRIVATE keepalive\n\n",
					`data: {"type":"response.output_text.delta","delta":"PRIVATE"}` + "\n\n",
				} {
					before := output.Body.Len()
					if _, err := io.WriteString(upstream, frame); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if output.Body.Len() <= before || strings.Contains(output.Body.String(), "PRIVATE") || stored {
						t.Fatalf("progress missing, leaked content or published early: %s", output.Body)
					}
					if index > 0 && !strings.Contains(output.Body.String()[before:], `"type":"response.in_progress"`) {
						t.Fatal("upstream activity did not produce a host data event")
					}
				}
				if outcome == "canceled" {
					cancel()
				}
				if outcome == "disconnected" {
					disconnected.failed = true
				}
				terminal := `{"type":"response.completed","response":{"id":"summary-response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"PRIVATE summary"}]}]}}`
				if err := codexresponses.WriteEvent(upstream, []byte(terminal)); err != nil {
					t.Fatal(err)
				}
				err = <-done
				if expected := map[string]error{"storage failure": storeFailure, "canceled": context.Canceled, "disconnected": codexresponses.ErrDownstreamDisconnected}[outcome]; expected != nil && !errors.Is(err, expected) {
					t.Fatalf("compaction error=%v, want %v", err, expected)
				}
				if outcome == "completed" {
					if err != nil || !stored || !strings.Contains(output.Body.String(), `"encrypted_content":"retained-summary"`) || strings.Count(output.Body.String(), `"type":"response.created"`) != 1 {
						t.Fatalf("completion: stored=%v err=%v output=%s", stored, err, output.Body)
					}
				} else if err == nil || strings.Contains(output.Body.String(), `"type":"response.completed"`) || (stored != (outcome == "storage failure")) {
					t.Fatalf("failed compaction published success: stored=%v err=%v output=%s", stored, err, output.Body)
				}
			})
		})
	}
}

type compactDisconnectedOutput struct {
	*httptest.ResponseRecorder
	failed bool
}

func (w *compactDisconnectedOutput) Write(data []byte) (int, error) {
	if w.failed {
		return 0, codexresponses.ErrDownstreamDisconnected
	}
	return w.ResponseRecorder.Write(data)
}

func (w *compactDisconnectedOutput) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func TestCompactionCommittedHTTPFailures(t *testing.T) {
	const completed = "data: " + `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}}` + "\n\n"
	const failed = "data: " + `{"type":"response.failed","response":{"id":"r","status":"failed","output":[],"error":{"message":"producer failed"}}}` + "\n\n"
	const created = "data: " + `{"type":"response.created","response":{"id":"r","output":[]}}` + "\n\n"
	storageFailure := errors.New("storage unavailable")
	for _, tc := range []struct {
		name        string
		body        io.Reader
		producerErr error
		want        error
	}{
		{"storage without creation", strings.NewReader(completed), nil, storageFailure},
		{"read without creation", compactReadFailure{}, nil, io.ErrUnexpectedEOF},
		{"read after creation", io.MultiReader(strings.NewReader(created), compactReadFailure{}), nil, io.ErrUnexpectedEOF},
		{"bridged timeout", strings.NewReader(failed), httptransport.ErrStreamIdleTimeout, httptransport.ErrStreamIdleTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := Executor{
				Provider: compactProvider(func([]byte, http.Header) *http.Response {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: compactProducerFailure{Reader: tc.body, err: tc.producerErr}}
				}),
				CompactPrompt: func() string { return "Summarize." },
				CompactStore:  func(context.Context, string, string, string) (string, error) { return "", storageFailure },
			}
			handler := inferencegateway.ResponsesHandler(t.Context(), time.Minute, func(start, execution context.Context, request codexresponses.Request, headers http.Header, output io.Writer) error {
				err := executor.ExecuteHTTP(start, execution, request, headers, output)
				if !errors.Is(err, tc.want) {
					t.Errorf("compaction error=%v, want %v", err, tc.want)
				}
				return err
			})
			request := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"gpt-6-astra","stream":true,"input":[],"tools":[]}`))
			request.Header.Set(codexresponses.ThreadIDHeader, "thread")
			request.Header.Set(codexresponses.TurnMetadataHeader, `{"thread_id":"thread","turn_id":"compact-turn","request_kind":"compaction","compaction":{"implementation":"responses_compaction_v2"}}`)
			output := httptest.NewRecorder()
			handler.ServeHTTP(output, request)
			if output.Code != http.StatusOK || strings.Count(output.Body.String(), `"type":"response.failed"`) != 1 || strings.Contains(output.Body.String(), `"id":""`) {
				t.Fatalf("committed stream lost or duplicated failure: %d %s", output.Code, output.Body)
			}
		})
	}
}

type compactReadFailure struct{}

func (compactReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type compactProducerFailure struct {
	io.Reader
	err error
}

func (compactProducerFailure) Close() error           { return nil }
func (b compactProducerFailure) TerminalError() error { return b.err }
