package requestexecution

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

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
