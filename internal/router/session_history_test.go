package router

import (
	"encoding/json/jsontext"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func confirmedHistoryExchange(t *testing.T, input []jsontext.Value, automatic bool) *codexresponses.SessionExchange {
	t.Helper()
	var session codexresponses.SessionHistory
	parent, request, err := session.Prepare(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "input": input}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.BeginProvider(request.Fields); err != nil {
		t.Fatal(err)
	}
	terminal := []byte(`{"type":"response.completed","response":{"id":"parent","output":[]}}`)
	if err := parent.ObserveProvider(terminal); err != nil {
		t.Fatal(err)
	}
	if err := parent.ObserveOutput(terminal); err != nil {
		t.Fatal(err)
	}
	command := responses.MustMarshalJSON(map[string]any{"previous_response_id": "parent", "input": []any{}})
	var first []byte
	if automatic {
		command = nil
		first = []byte(`{"type":"response.created","response":{"id":"next","previous_response_id":"parent"}}`)
	}
	exchange, _, err := session.Prepare(command, first)
	if err != nil {
		t.Fatal(err)
	}
	return exchange
}
