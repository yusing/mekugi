package launch

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/contracts/inference"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestResponseHooksProviderUsage(t *testing.T) {
	var observed []inference.Usage
	hooks := &requestexecution.ResponseHooks{OnUsage: func(counts inference.Usage) { observed = append(observed, counts) }}
	for _, payload := range []string{
		`{"type":"response.output_item.done","item":{"type":"function_call"}}`,
		`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`,
		`[DONE]`,
	} {
		hooks.Observe([]byte(payload), true)
	}
	if len(observed) != 1 || observed[0].InputTokens != 2 || observed[0].OutputTokens != 1 {
		t.Fatalf("provider usage = %+v", observed)
	}
}

func TestResponseHooksPreserveSSEHeartbeat(t *testing.T) {
	const wire = ": ping\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"
	var output bytes.Buffer
	state, err := copySSETransformed(&output, strings.NewReader(wire), nil, &requestexecution.ResponseHooks{})
	if err != nil || state != responseevents.TerminalCompleted || output.String() != wire {
		t.Fatalf("state=%v error=%v output=%q", state, err, output.String())
	}
}

func BenchmarkProviderObservation(b *testing.B) {
	const wire = "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":10}}}\n\n"
	b.ReportAllocs()
	for b.Loop() {
		hooks := &requestexecution.ResponseHooks{OnUsage: func(inference.Usage) {}}
		if _, err := copySSETransformed(io.Discard, strings.NewReader(wire), nil, hooks); err != nil {
			b.Fatal(err)
		}
	}
}
