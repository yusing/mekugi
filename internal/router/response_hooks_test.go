package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResponseHooksFinishOnce(t *testing.T) {
	var results []requestCompletion
	hooks := &responseHooks{onFinished: func(r requestCompletion) { results = append(results, r) }}
	first := requestCompletion{outcome: requestOutcomeCompleted, terminal: responseTerminalSteered}
	hooks.finish(first)
	hooks.finish(requestCompletion{outcome: requestOutcomeCompleted, terminal: responseTerminalCompleted})
	if len(results) != 1 || results[0] != first || results[0].succeeded() || !results[0].acceptsOutput() {
		t.Fatalf("finish results = %#v", results)
	}
}

func TestResponseHooksProviderObservationOrder(t *testing.T) {
	var order []string
	hooks := &responseHooks{
		onUsage: func(tokenCounts) { order = append(order, "usage") },
		output:  &hookOutputRecorder{order: &order},
	}
	for _, payload := range []string{
		`{"type":"response.output_item.done","item":{"type":"function_call"}}`,
		`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":2}}}`,
		`[DONE]`,
	} {
		if err := hooks.observe([]byte(payload), true); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(order, ","); got != "item,usage,snapshot" {
		t.Fatal(got)
	}
}

type hookOutputRecorder struct{ order *[]string }

func (r *hookOutputRecorder) outputItemDone(json.RawMessage) { *r.order = append(*r.order, "item") }
func (r *hookOutputRecorder) completedOutput([]json.RawMessage) {
	*r.order = append(*r.order, "snapshot")
}

func TestResponseHooksPreserveMalformedOutputRejection(t *testing.T) {
	for _, payload := range []string{` [DONE] `, `{"type":"response.completed","response":{"output":42}}`} {
		hooks := &responseHooks{output: &mentorResponseObservation{}}
		var output bytes.Buffer
		_, err := copySSETransformed(&output, strings.NewReader("data: "+payload+"\n\n"), nil, hooks)
		if !errors.Is(err, errResponseTransform) {
			t.Fatalf("accepted malformed output %q: %v", payload, err)
		}
	}
}

func TestResponseHooksPreserveSSEHeartbeat(t *testing.T) {
	const wire = ": ping\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"
	var observation mentorResponseObservation
	hooks := &responseHooks{}
	hooks.output = &observation
	var output bytes.Buffer
	state, err := copySSETransformed(&output, strings.NewReader(wire), nil, hooks)
	if err != nil || state != responseTerminalCompleted || output.String() != wire || observation.toolCalls != 1 {
		t.Fatalf("state=%v error=%v output=%q observation=%+v", state, err, output.String(), observation)
	}
}
