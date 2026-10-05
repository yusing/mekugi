package capturer

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func streamMeterEvent(t *testing.T, kind, text string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"type": kind, "output_index": 0, "delta": text})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestStreamOutputMeterEmission(t *testing.T) {
	text := strings.Repeat("Let us inspect the implementation carefully. ", 16)
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := codec.Count(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"response.output_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta"} {
		t.Run(kind, func(t *testing.T) {
			var meter StreamOutputMeter
			if got := meter.Observe(streamMeterEvent(t, kind, text), 2*time.Second); got != float64(tokens)/2 {
				t.Fatalf("rate=%g, want %g", got, float64(tokens)/2)
			}
			// Finalized output repeats already emitted text, not new output.
			if got := meter.Observe([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"exec","arguments":"repeated"}}`), 3*time.Second); got != 0 {
				t.Fatalf("finalized item double-counted: %g", got)
			}
		})
	}
}

func TestStreamOutputMeterReasoningThreshold(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		elapsed time.Duration
	}{
		{"fast", strings.Repeat("reasoning ", 64), 999 * time.Millisecond},
		{"short", "check the source", 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var meter StreamOutputMeter
			if got := meter.Observe(streamMeterEvent(t, "response.reasoning_summary_text.delta", tc.text), tc.elapsed); got != 0 {
				t.Fatalf("reasoning sample not skipped: %g", got)
			}
		})
	}
	var meter StreamOutputMeter
	text := strings.Repeat("reasoning ", 64)
	if got := meter.Observe(streamMeterEvent(t, "response.reasoning_summary_text.delta", text), time.Second); got <= 0 {
		t.Fatal("substantial reasoning was skipped")
	}
}

func TestStreamOutputMeterFramingAndBounds(t *testing.T) {
	text := strings.Repeat("日本語の output ", 4000)
	var whole, fragmented StreamOutputMeter
	want := whole.Observe(streamMeterEvent(t, "response.function_call_arguments.delta", text), 2*time.Second)
	var got float64
	for _, fragment := range strings.SplitAfter(text, " ") {
		if sample := fragmented.Observe(streamMeterEvent(t, "response.function_call_arguments.delta", fragment), 2*time.Second); sample > 0 {
			got = sample
		}
	}
	// Flush the sampled tail after the sampling interval.
	got = fragmented.Observe(streamMeterEvent(t, "response.function_call_arguments.delta", " end"), 3*time.Second)
	want = whole.Observe(streamMeterEvent(t, "response.function_call_arguments.delta", " end"), 3*time.Second)
	if got != want || got <= 0 || len(fragmented.pending) >= streamTokenWindow {
		t.Fatalf("fragmented=%g whole=%g pending=%d", got, want, len(fragmented.pending))
	}
}

func TestStreamOutputMeterCompleteToolEmission(t *testing.T) {
	for _, item := range []string{
		`{"type":"function_call","name":"exec","arguments":"some complete arguments"}`,
		`{"type":"custom_tool_call","name":"apply_patch","input":"some complete patch"}`,
	} {
		var meter StreamOutputMeter
		// Empty framing fragments must not hide the complete tool emission.
		meter.Observe(streamMeterEvent(t, "response.function_call_arguments.delta", ""), time.Second/2)
		payload := []byte(`{"type":"response.output_item.done","output_index":0,"item":` + item + `}`)
		if got := meter.Observe(payload, time.Second); got <= 0 {
			t.Fatal("complete tool emission was skipped")
		}
		if got := meter.Observe(payload, 2*time.Second); got != 0 {
			t.Fatal("repeated item was counted")
		}
		meter.Observe([]byte(`{"type":"response.completed","response":{"output":[]}}`), 3*time.Second)
		if got := meter.Observe(streamMeterEvent(t, "response.output_text.delta", "late output"), 4*time.Second); got != 0 {
			t.Fatal("terminal measurement was replaced by late output")
		}
	}
}
