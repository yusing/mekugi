package launch

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/grok"
)

func TestGrokStreamFailureTerminal(t *testing.T) {
	for _, test := range []struct {
		name, stream, code string
	}{
		{"provider error", "data: {\"error\":{\"message\":\"private-provider-content\"}}\n\n", "grok_stream_provider_error"},
		{"invalid JSON", "data: private-provider-content\n\n", "grok_stream_invalid_json"},
		{"missing marker", strings.TrimSuffix(grokTextStream(), "data: [DONE]\n\n"), "grok_stream_missing_done"},
		{"missing calls", grokTestSSE(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls"}}}), "grok_stream_missing_calls"},
		{"post-terminal choice", grokTestSSE(
			map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop"}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop"}}},
		), "grok_stream_data_after_terminal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []map[string]any
			state, err := readGrokTestEvents(t, grokTestRequest(t, true), strings.NewReader(test.stream), func(event map[string]any) error {
				events = append(events, event)
				return nil
			})
			diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](err)
			if state != responses.TerminalFailed || !ok || diagnostic.Code != test.code {
				t.Fatalf("state=%v err=%v", state, err)
			}
			created := events[0]["response"].(map[string]any)
			last := events[len(events)-1]
			failed := last["response"].(map[string]any)
			if last["type"] != "response.failed" || failed["status"] != "failed" || failed["id"] != created["id"] {
				t.Fatalf("invalid failed terminal: %v", last)
			}
			if failed["error"].(map[string]any)["code"] != test.code {
				t.Fatalf("missing failure code: %v", failed)
			}
			for _, event := range events {
				switch event["type"] {
				case "response.completed", "response.output_item.done", "response.function_call_arguments.done", "response.custom_tool_call_input.done":
					t.Fatalf("failure exposed successful output: %v", event)
				}
			}
			notices := testCriticalErrors()
			finalization := notices.Report(requestexecution.Failure{Session: "test", Phase: "inspect_response"}, err)
			if finalization.Code != test.code {
				t.Fatalf("unclassified request failure: %s", finalization.Code)
			}
			pending := strings.Join(notices.Notices.Pending(), "\n")
			want := diagnostic.Summary
			if test.name == "provider error" {
				want = "private-provider-content"
			}
			if !strings.Contains(pending, want) || strings.Contains(pending, "not safe for display") {
				t.Fatalf("invalid notice: %s", pending)
			}
			if test.name != "provider error" && bytes.Contains(responses.MustMarshalJSON(events), []byte("private-provider-content")) {
				t.Fatal("provider content leaked into failure events")
			}
		})
	}
}

func TestGrokStreamWriteFailureDoesNotRetry(t *testing.T) {
	for _, failAt := range []int{1, 2, 4} {
		writes := 0
		failure := requestexecution.StaticError("test_write", "consumer write failed")
		_, err := readGrokTestEvents(t, grokTestRequest(t, true), strings.NewReader(grokTextStream()), func(map[string]any) error {
			writes++
			if writes >= failAt {
				return failure
			}
			return nil
		})
		if !errors.Is(err, failure) || writes != failAt {
			t.Fatalf("failAt=%d writes=%d err=%v", failAt, writes, err)
		}
	}
}

func TestGrokClientDeliversFailureBeforeReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"error\":{\"message\":\"private-provider-content\"}}\n\n")
	}))
	defer server.Close()
	client := &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics(), HTTPClient: grokTestHTTPClient(t, server), Auth: grok.NewAuth("", "xai-test", httptransport.Client(nil))}
	response, err := client.ForwardExecution(t.Context(), t.Context(), grokTestRequest(t, true), grokTestHeaders())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var output bytes.Buffer
	state, err := requestexecution.CopyResponse(&output, response.Response, true, nil, nil)
	body := output.Bytes()
	if state != responses.TerminalFailed {
		t.Fatalf("terminal = %v", state)
	}
	diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](err)
	if !ok || diagnostic.Code != "grok_stream_provider_error" {
		t.Fatalf("lost producer failure: %v", err)
	}
	if !bytes.Contains(body, []byte("event: response.failed\n")) ||
		bytes.Contains(body, []byte("response.completed")) ||
		!bytes.Contains(body, []byte("private-provider-content")) {
		t.Fatalf("invalid failure stream: %s", body)
	}
}

func TestGrokStreamReadFailurePreservesCause(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	reader := io.MultiReader(strings.NewReader("data: {\"choices\":[]}\n\n"), grokFailureReader{cause})
	var last map[string]any
	_, err := readGrokTestEvents(t, grokTestRequest(t, true), reader, func(event map[string]any) error {
		last = event
		return nil
	})
	if !errors.Is(err, cause) || last["type"] != "response.failed" {
		t.Fatalf("last=%v err=%v", last, err)
	}
	failed := last["response"].(map[string]any)
	if failed["error"].(map[string]any)["code"] != "upstream_unexpected_eof" {
		t.Fatalf("lost transport classification: %v", failed)
	}
}

type grokFailureReader struct{ err error }

func (r grokFailureReader) Read([]byte) (int, error) { return 0, r.err }

func TestGrokClientFailedTerminalRetainsTimeoutAccounting(t *testing.T) {
	client := &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics(), Auth: grok.NewAuth("", "test", httptransport.Client(nil)), HTTPClient: &http.Client{
		Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:   io.NopCloser(grokFailureReader{httptransport.ErrStreamIdleTimeout})}, nil
		}),
	}}
	response, err := client.ForwardExecution(t.Context(), t.Context(), grokTestRequest(t, true), grokTestHeaders())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	state, err := requestexecution.CopyResponse(&output, response.Response, true, nil, nil)
	if state != responses.TerminalFailed || !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
		t.Fatalf("state=%v err=%v", state, err)
	}

}
