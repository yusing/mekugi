package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestResponsesWebSocketSteeringAdmissionHistory(t *testing.T) {
	input := func(text string) []json.RawMessage {
		items, err := webSocketInput(responses.MustMarshalJSON(text))
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	s := &SessionHistory{steers: []pendingSteer{
		{id: "rejected", parent: "parent", input: input("must not be retained")},
		{parent: "parent", input: input("accepted after continuation send")},
		{id: "other", parent: "other-parent", input: input("different parent")},
	}}
	history := &historyNode{input: input("tool continuation")}
	s.ObserveControl([]byte(`{"type":"response.steer.failed","steer":{"id":"rejected","previous_response_id":"parent"}}`))
	s.ObserveControl([]byte(`{"type":"response.steer.accepted","steer":{"id":"late","previous_response_id":"parent"}}`))
	s.commitSteering(history, "parent")
	got := string(responses.MustMarshalJSON(history.input))
	if strings.Contains(got, "must not be retained") || strings.Contains(got, "different parent") ||
		!strings.Contains(got, "accepted after continuation send") || len(history.input) != 2 {
		t.Fatalf("admitted history = %s", got)
	}
	if len(s.steers) != 1 || s.steers[0].id != "other" {
		t.Fatalf("wrong steering submissions consumed: %#v", s.steers)
	}
	again := &historyNode{}
	s.commitSteering(again, "parent")
	if len(again.input) != 0 {
		t.Fatal("accepted steering replayed twice")
	}
}

func TestResponsesWebSocketTerminalOutputBudget(t *testing.T) {
	streamed := json.RawMessage(`{"type":"message","id":"item","text":"streamed"}`)
	replacement := json.RawMessage(`{"type":"message","id":"item","text":"final metadata"}`)
	commentary := json.RawMessage(`{"type":"message","id":"commentary","text":"progress"}`)
	for _, tc := range []struct {
		name      string
		output    []json.RawMessage
		want      []json.RawMessage
		remaining int
		wantError bool
	}{
		{"same output at budget", []json.RawMessage{streamed}, []json.RawMessage{streamed}, 0, false},
		{"matching item updates metadata", []json.RawMessage{replacement}, []json.RawMessage{replacement}, len(replacement) - len(streamed), false},
		{"terminal-only addition preserves streamed", []json.RawMessage{commentary}, []json.RawMessage{streamed, commentary}, len(commentary), false},
		{"partial snapshot updates and preserves", []json.RawMessage{commentary, replacement}, []json.RawMessage{replacement, commentary}, len(commentary) + len(replacement) - len(streamed), false},
		{"addition exceeds budget", []json.RawMessage{commentary}, []json.RawMessage{streamed}, 0, true},
		{"empty preserves streamed", nil, []json.RawMessage{streamed}, 0, false},
		{"replacement exceeds budget", []json.RawMessage{replacement}, []json.RawMessage{streamed}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &historyNode{output: []json.RawMessage{streamed}}
			initial := MaxResponseBytes - tc.remaining
			s := &SessionHistory{retainedBytes: initial, histories: make(map[string]*historyNode)}
			exchange := &SessionExchange{session: s, history: history}
			err := exchange.ObserveOutput(responses.MustMarshalJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "done", "output": tc.output}}))
			if (err != nil) != tc.wantError {
				t.Fatalf("terminal error = %v", err)
			}
			if !bytes.Equal(responses.MustMarshalJSON(history.output), responses.MustMarshalJSON(tc.want)) {
				t.Fatalf("retained output = %s", responses.MustMarshalJSON(history.output))
			}
			wantBytes := initial
			if !tc.wantError {
				wantBytes -= len(streamed)
				for _, item := range tc.want {
					wantBytes += len(item)
				}
			}
			if s.retainedBytes != wantBytes {
				t.Fatalf("retained bytes = %d, want %d", s.retainedBytes, wantBytes)
			}
		})
	}
}
