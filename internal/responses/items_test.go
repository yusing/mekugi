package responses

import "testing"

func TestMessageFacts(t *testing.T) {
	for _, test := range []struct {
		name               string
		facts              MessageFacts
		answer, commentary bool
	}{
		{"legacy answer", MessageFacts{Kind: Message, Role: "assistant"}, true, false},
		{"answer", MessageFacts{Kind: Message, Role: "assistant", Phase: "final_answer"}, true, false},
		{"commentary", MessageFacts{Kind: Message, Role: "assistant", Phase: "commentary", Status: "completed"}, false, true},
		{"unfinished commentary", MessageFacts{Kind: Message, Role: "assistant", Phase: "commentary"}, false, false},
		{"unknown phase", MessageFacts{Kind: Message, Role: "assistant", Phase: "future"}, false, false},
		{"user", MessageFacts{Kind: Message, Role: "user"}, false, false},
		{"missing role", MessageFacts{Kind: Message}, false, false},
		{"tool", MessageFacts{Kind: FunctionCall, Role: "assistant"}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.facts.FinalAnswerCandidate() != test.answer || test.facts.CompletedCommentary() != test.commentary {
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
