package requestexecution

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRequestWireProjectsStandaloneToolContextWithoutPreparer(t *testing.T) {
	for _, model := range []string{"gpt-6-sol"} {
		request, err := codexresponses.ParseRequest([]byte(`{"model":"` + model + `","input":[{"type":"function_call_output","namespace":"threadcontrol","name":"spawn_agent","output":"Assignment retained."}]}`))
		if err != nil {
			t.Fatal(err)
		}
		original := bytes.Clone(request.OriginalField("input"))
		attempt := newRequestAttempt(Executor{}, t.Context(), t.Context(), request, http.Header{}, "")
		if err := attempt.prepareWire(); err != nil {
			t.Fatal(err)
		}
		var sent struct {
			Input []map[string]jsontext.Value `json:"input"`
		}
		if err := json.Unmarshal(attempt.forwardBody, &sent); err != nil || len(sent.Input) != 1 ||
			responses.StringField(sent.Input[0], "role") != "assistant" || bytes.Contains(attempt.forwardBody, []byte(`"function_call_output"`)) ||
			!bytes.Contains(attempt.forwardBody, []byte("Assignment retained.")) || !bytes.Equal(original, request.OriginalField("input")) {
			t.Fatalf("wire context was not adapted while preserving native history: %s, %v", attempt.forwardBody, err)
		}
	}
}

func TestNativeSourceReceiptDoesNotReachProviderAPIs(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "custom"}[custom], func(t *testing.T) {
			requests := make(chan []byte, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"receipt-forwarding\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer provider.Close()
			gateway := inferencegateway.NewClient(provider.URL, provider.Client(), ProviderDiagnostics())
			if custom {
				gateway.Native = &inferencegateway.NativeProvider{ID: "custom", BaseURL: provider.URL, HTTPClient: provider.Client()}
			}
			request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-6-sol","input":[{"type":"function_call_output","namespace":"context","name":"reload","output":"Exact supplied source","internal_chat_message_metadata_passthrough":{"source_acquisition":{"id":"private-receipt"},"turn_id":"original-turn"}}]}`))
			if err != nil {
				t.Fatal(err)
			}
			executor := Executor{Provider: gateway, Output: io.Discard, Issues: testCriticalErrors()}
			headers := http.Header{"Authorization": {"Bearer fixture"}, "Chatgpt-Account-Id": {"fixture"}}
			if err := executor.Execute(t.Context(), t.Context(), request, headers, "session"); err != nil {
				t.Fatal(err)
			}
			forwarded := <-requests
			if len(forwarded) == 0 || bytes.Contains(forwarded, []byte("source_acquisition")) || bytes.Contains(forwarded, []byte("private-receipt")) || !bytes.Contains(forwarded, []byte("Exact supplied source")) || !bytes.Contains(forwarded, []byte("original-turn")) {
				t.Fatalf("provider received private facts or lost unrelated context: %s", forwarded)
			}
		})
	}
}

func TestRequestAttemptFinishDoesNotDuplicateDeliveredTerminal(t *testing.T) {
	output := new(bytes.Buffer)
	for _, event := range []string{
		`{"type":"response.created","response":{"id":"already-failed-response","status":"in_progress"}}`,
		`{"type":"response.failed","response":{"id":"already-failed-response","status":"failed","error":{"code":"invalid_prompt","message":"prior terminal failure"}}}`,
	} {
		if err := codexresponses.WriteEvent(output, []byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	attempt := &requestAttempt{
		executor:       Executor{Output: output, Issues: testCriticalErrors()},
		startCtx:       t.Context(),
		executionCtx:   t.Context(),
		hooks:          &ResponseHooks{deliveredResponseID: "already-failed-response", deliveredTerminal: true},
		streamResponse: true,
		finalization: requestFinalization{
			sessionID: "already-failed-session", threadID: "already-failed-thread", turnID: "already-failed-turn",
			failurePhase: requestFailureTransform,
		},
	}
	if err := attempt.finish(errors.New("a transform fault arrived after terminal delivery")); err == nil {
		t.Fatal("request finish lost the transform fault")
	}
	failed := strings.Count(output.String(), `"type":"response.failed"`)
	if failed != 1 {
		t.Fatalf("response.failed count = %d, want no duplicate terminal: %s", failed, output.String())
	}
}

func TestRequestAttemptFinishPreservesUncreatedOrdinaryFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	output := &inferencegateway.ResponseWriter{ResponseWriter: recorder}
	if err := http.NewResponseController(output).Flush(); err != nil {
		t.Fatal(err)
	}
	attempt := &requestAttempt{
		executor: Executor{Output: output, Issues: testCriticalErrors()},
		startCtx: t.Context(), executionCtx: t.Context(), hooks: new(ResponseHooks),
		finalization: requestFinalization{failurePhase: requestFailureTransform},
	}
	if err := attempt.finish(errors.New("transform failed before creation")); err == nil {
		t.Fatal("request finish lost the transform fault")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("ordinary failure synthesized an uncreated terminal: %s", recorder.Body)
	}
}
