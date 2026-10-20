package responses

import "testing"

func TestTerminalEvidence(t *testing.T) {
	for _, test := range []struct {
		payload string
		stream  bool
		want    TerminalState
	}{
		{`{"type":"response.completed","response":{"status":"failed"}}`, true, TerminalCompleted},
		{`{"status":"failed"}`, false, TerminalFailed},
		{`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"steered"}}}`, true, TerminalSteered},
		{`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"interrupted"}}}`, true, TerminalInterrupted},
		{`{"status":"incomplete","incomplete_details":{"reason":"interrupted"}}`, false, TerminalFailed},
		{`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, true, TerminalFailed},
		{`{"status":"incomplete","incomplete_details":{"reason":"steered"}}`, false, TerminalFailed},
		{`{"type":"response.incomplete","response":{"incomplete_details":42}}`, true, TerminalFailed},
		{`{"type":"codex.response.metadata"}`, true, TerminalUnknown},
		{`{"type":"response.future"}`, true, TerminalPending},
		{`{"type":"response."}`, true, TerminalInvalid},
		{`{"type":"error"}`, true, TerminalFailed},
		{`{"type":"error","status":429,"error":{"code":"rate_limit_exceeded"}}`, true, TerminalFailed},
		{`{"type":42}`, true, TerminalInvalid},
		{`{"type":42,"status":"completed"}`, false, TerminalUnknown},
		{`[DONE]`, true, TerminalUnknown},
		{``, true, TerminalUnknown},
		{`null`, true, TerminalInvalid},
	} {
		t.Run(test.payload, func(t *testing.T) {
			if got := ObserveTerminal([]byte(test.payload), test.stream); got != test.want {
				t.Fatalf("%v != %v", got, test.want)
			}
		})
	}
	if !Kind(Error).EndsExchange() || Kind(Error).Terminal() || Kind("response.steer.pending").EndsExchange() || !Kind("response.steer.pending").Steering() {
		t.Fatal("exchange, response and steering boundaries conflated")
	}
	for _, sticky := range []TerminalState{TerminalInvalid, TerminalFailed, TerminalSteered, TerminalInterrupted} {
		if got := MergeTerminal(sticky, TerminalCompleted); got != sticky {
			t.Fatalf("lost %v: %v", sticky, got)
		}
	}
	for _, sticky := range []TerminalState{TerminalInvalid, TerminalFailed} {
		if MergeTerminal(sticky, TerminalInterrupted) != sticky || MergeTerminal(TerminalInterrupted, sticky) != sticky {
			t.Fatalf("interruption erased %v evidence", sticky)
		}
	}
	if !TerminalInterrupted.Terminal() || TerminalInterrupted.String() != "interrupted" {
		t.Fatal("interruption is not a distinct terminal")
	}
}

func BenchmarkObserveTerminal(b *testing.B) {
	payload := []byte(`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":100,"output_tokens":10}}}`)
	b.ReportAllocs()
	for b.Loop() {
		ObserveTerminal(payload, true)
	}
}

func TestEventFamilies(t *testing.T) {
	for _, kind := range []Kind{FunctionArgumentsDelta, FunctionArgumentsDone} {
		if !kind.FunctionArguments() || kind.ItemEvent() || kind.Terminal() {
			t.Fatal(kind)
		}
	}
	for _, kind := range []Kind{OutputItemAdded, OutputItemDone} {
		if !kind.ItemEvent() {
			t.Fatal(kind)
		}
	}
	for _, kind := range []Kind{ContentPartAdded, ContentPartDone} {
		if kind.ItemEvent() {
			t.Fatal(kind)
		}
	}
	for _, kind := range []Kind{"response.function_call_arguments.future", "response.output_item.future", "response.content_part.future"} {
		if kind.FunctionArguments() || kind.ItemEvent() {
			t.Fatal(kind)
		}
	}
}
