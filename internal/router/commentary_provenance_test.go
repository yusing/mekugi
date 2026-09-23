package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCommentaryReplayFilteringRequiresExactRetainedID(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	known := subagentCommentaryMessageID("known")
	unknown := subagentCommentaryMessageID("unknown")
	if err := store.putCommentary(t.Context(), "workspace", []string{known}); err != nil {
		t.Fatal(err)
	}

	request := &parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{
			assistantCommentaryMessage(known, "router-authored"),
			assistantCommentaryMessage(unknown, "model-authored"),
		}),
	}}
	proxy := &mekugiProxy{replayStore: store}
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, "workspace", "session"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.fields["input"], []byte(known)) {
		t.Fatalf("known router commentary survived replay: %s", request.fields["input"])
	}
	if !bytes.Contains(request.fields["input"], []byte(unknown)) {
		t.Fatalf("unretained prefix-matching message was removed: %s", request.fields["input"])
	}
}

func TestSubagentProjectionPreservesUnretainedPrefixMessage(t *testing.T) {
	envelope := map[string]any{
		"type": "agent_message", "id": "reply", "author": "/root/a", "recipient": "/root/b",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": "Message Type: MESSAGE\nTask name: /root/b\nSender: /root/a\nPayload:\nresult",
		}},
	}
	projectedID := subagentCommentaryMessageID("response\x00reply\x00/root/a\x00result")
	unknown := assistantCommentaryMessage(projectedID, "model-authored")
	fields := map[string]json.RawMessage{"input": mustTestJSON(t, []any{unknown, envelope})}
	original := bytes.Clone(fields["input"])

	if messages := prepareSubagentInputEnvelopes(fields, "/root/b").commentary; len(messages) != 0 {
		t.Fatalf("visible exact ID was projected twice: %s", mustTestJSON(t, messages))
	}
	if !bytes.Equal(fields["input"], original) {
		t.Fatalf("projection changed original envelopes: %s", fields["input"])
	}
}
