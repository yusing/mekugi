package responses

import (
	"encoding/json"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestProviderHistoryReconciliation(t *testing.T) {
	original := []json.RawMessage{
		json.RawMessage(`{"role":"developer","content":"rules"}`),
		json.RawMessage(`{"role":"user","content":"work"}`),
		json.RawMessage(`{"type":"function_call","call_id":"lookup-call","name":"lookup","arguments":"{}"}`),
	}
	state, err := (providerHistory{confirmed: true}).append(original)
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"type":"function_call_output","call_id":"lookup-call","output":"done"}`)
	for _, tc := range []struct {
		name       string
		input      []json.RawMessage
		confirmed  bool
		automatic  bool
		parent     bool
		wantCached int
		wantRebase bool
		wantError  bool
	}{
		{"missing result", append(append([]json.RawMessage{}, original...), result), true, false, true, 3, false, false},
		{"same prefix", original, true, false, true, 3, false, false},
		{"changed noninstruction", []json.RawMessage{original[0], json.RawMessage(`{"role":"user","content":"changed"}`), original[2]}, true, false, true, 0, true, false},
		{"shortened", original[:2], true, false, true, 0, true, false},
		{"unconfirmed", original, false, false, true, 0, true, false},
		{"fresh request", original, true, false, false, 0, false, false},
		{"automatic unchanged", original, true, true, true, 3, false, false},
		{"automatic missing result", append(append([]json.RawMessage{}, original...), result), true, true, true, 3, false, true},
		{"automatic uncertain", original, false, true, true, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentState := state
			parentState.confirmed = tc.confirmed
			exchange := &SessionExchange{
				parentID: "parent", automatic: tc.automatic,
				history: &historyNode{parent: &historyNode{providerHistory: parentState}},
			}
			fields := map[string]json.RawMessage{"input": responses.MustMarshalJSON(tc.input)}
			if tc.parent {
				fields["previous_response_id"] = responses.MustMarshalJSON("parent")
			}
			// Stale native boundaries and feature-local rebase hints must not
			// override complete provider evidence.
			request := &Request{Fields: fields, CachedInput: 999, RebaseInput: true}
			_, err := exchange.Reconcile(request, true)
			if (err != nil) != tc.wantError {
				t.Fatalf("reconcile error: %v", err)
			}
			if request.CachedInput != tc.wantCached || request.RebaseInput != tc.wantRebase {
				t.Fatalf("cached=%d rebase=%v", request.CachedInput, request.RebaseInput)
			}
		})
	}
}

func TestProviderHistoryJSONIdentity(t *testing.T) {
	fingerprint := func(raw string) providerHistory {
		t.Helper()
		h, err := (providerHistory{}).append([]json.RawMessage{json.RawMessage(raw)})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	base := fingerprint(`{"a":9007199254740993,"b":"text"}`)
	if base != fingerprint(`{ "b": "text", "a": 9007199254740993 }`) {
		t.Fatal("object order changed identity")
	}
	if base == fingerprint(`{"a":9007199254740992,"b":"text"}`) {
		t.Fatal("large JSON numbers lost precision")
	}
}

func TestProviderHistoryRequiresRawTerminal(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.failed", "response.incomplete", "error"} {
		t.Run(terminal, func(t *testing.T) {
			exchange := &SessionExchange{session: &SessionHistory{}, history: &historyNode{}}
			if err := exchange.BeginProvider(map[string]json.RawMessage{"input": json.RawMessage(`[{"role":"user","content":"work"}]`)}); err != nil {
				t.Fatal(err)
			}
			item := json.RawMessage(`{"type":"function_call","id":"item","call_id":"call","name":"lookup","arguments":"{}"}`)
			if err := exchange.ObserveProvider(responses.MustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": item})); err != nil {
				t.Fatal(err)
			}
			if exchange.history.providerHistory.confirmed {
				t.Fatal("streamed output confirmed history before terminal")
			}
			if err := exchange.ObserveProvider(responses.MustMarshalJSON(map[string]any{"type": terminal, "response": map[string]any{"id": "done", "output": []any{}}})); err != nil {
				t.Fatal(err)
			}
			got := exchange.history.providerHistory
			if got.confirmed != (terminal == "response.completed") {
				t.Fatalf("terminal confirmation = %v", got.confirmed)
			}
			if got.confirmed && got.count != 2 {
				t.Fatalf("empty snapshot discarded streamed call: count=%d", got.count)
			}
			if exchange.session.retainedBytes != 0 {
				t.Fatal("temporary output retention leaked")
			}
		})
	}
}

func TestProviderHistoryBranchAndFreshConnection(t *testing.T) {
	input := []json.RawMessage{json.RawMessage(`{"role":"user","content":"ancestor"}`)}
	state, err := (providerHistory{confirmed: true}).append(input)
	if err != nil {
		t.Fatal(err)
	}
	ancestor := &historyNode{providerHistory: state}
	branch := &SessionExchange{history: &historyNode{parent: ancestor}, parentID: "ancestor", session: &SessionHistory{}}
	if err := branch.BeginProvider(map[string]json.RawMessage{"previous_response_id": responses.MustMarshalJSON("ancestor"), "input": json.RawMessage(`[{"role":"user","content":"branch"}]`)}); err != nil {
		t.Fatal(err)
	}
	if err := branch.ObserveProvider([]byte(`{"type":"response.completed","response":{"id":"branch","output":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if ancestor.providerHistory != state {
		t.Fatal("branch changed ancestor fingerprint")
	}
	// Visible native history can survive restart, but transport confirmation
	// cannot. An explicit full-history request remains usable on a fresh socket.
	fresh := &SessionExchange{history: &historyNode{input: input}}
	fields := map[string]json.RawMessage{"input": responses.MustMarshalJSON(input)}
	request := &Request{Fields: fields}
	if _, err := fresh.Reconcile(request, true); err != nil {
		t.Fatal(err)
	}
	if request.CachedInput != 0 || request.RebaseInput {
		t.Fatal("fresh connection borrowed retained provider state")
	}
}
