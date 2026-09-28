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
