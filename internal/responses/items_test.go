package responses

import "testing"

func TestMessageFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		facts  MessageFacts
		answer bool
	}{
		{"legacy answer", MessageFacts{Kind: Message, Role: "assistant"}, true},
		{"answer", MessageFacts{Kind: Message, Role: "assistant", Phase: "final_answer"}, true},
		{"commentary", MessageFacts{Kind: Message, Role: "assistant", Phase: "commentary", Status: "completed"}, false},
		{"unfinished commentary", MessageFacts{Kind: Message, Role: "assistant", Phase: "commentary"}, false},
		{"unknown phase", MessageFacts{Kind: Message, Role: "assistant", Phase: "future"}, false},
		{"user", MessageFacts{Kind: Message, Role: "user"}, false},
		{"missing role", MessageFacts{Kind: Message}, false},
		{"tool", MessageFacts{Kind: FunctionCall, Role: "assistant"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.facts.FinalAnswerCandidate() != test.answer {
				t.Fatal(test.facts)
			}
		})
	}
	if !ItemKind(FunctionCall).ToolCall() || !ItemKind(CustomToolCall).ToolCall() || ItemKind("future_call").ToolCall() {
		t.Fatal("call encodings changed")
	}
}

func TestCorrelationAndCaptureAreNotTransportAcceptance(t *testing.T) {
	if !Kind("response.").ResponseFamily() || Kind("response.").ResponseEvent() {
		t.Fatal("correlation prefix became transport acceptance")
	}
	if !TerminalStatus("cancelled") || ObserveTerminal([]byte(`{"status":"cancelled"}`), false) != TerminalUnknown {
		t.Fatal("capture cancellation evidence became transport acceptance")
	}
	if TerminalStatus("future") || RequestKind("future").Known() {
		t.Fatal("unknown wire value accepted")
	}
}
