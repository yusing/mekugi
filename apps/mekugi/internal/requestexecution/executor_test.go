package requestexecution

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRequestWireProjectsStandaloneToolContextWithoutPreparer(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "grok:grok-4.6", "opencode-go:test"} {
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
