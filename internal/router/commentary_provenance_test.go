package router

import (
	"bytes"
	"encoding/json"
	"testing"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestCommentaryReplayFilteringRequiresExactRetainedID(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	known := subagentCommentaryMessageID("known")
	unknown := subagentCommentaryMessageID("unknown")
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error {
		return store.write(replayRecord{Version: 1, Workspace: "workspace", CallID: known, Commentary: true})
	}); err != nil {
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

// assistantCommentaryMessage creates an assistant commentary message with the given ID and text.
func assistantCommentaryMessage(id, text string) map[string]json.RawMessage {
	type outputText struct {
		Annotations []any  `json:"annotations"`
		Text        string `json:"text"`
		Type        string `json:"type"`
	}
	type outputMessage struct {
		Type    string       `json:"type"`
		ID      string       `json:"id,omitempty"`
		Role    string       `json:"role"`
		Status  string       `json:"status"`
		Phase   string       `json:"phase"`
		Content []outputText `json:"content"`
	}
	encoded := responseevents.MustMarshalJSON(outputMessage{
		Role:   "assistant",
		Type:   "message",
		ID:     id,
		Status: "completed",
		Phase:  "commentary",
		Content: []outputText{{
			Type:        "output_text",
			Text:        text,
			Annotations: []any{},
		}},
	})
	var message map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &message); err != nil {
		panic(err)
	}
	return message
}

// assistantCommentaryDoneEvent creates a response.output_item.done event for a commentary message.
func assistantCommentaryDoneEvent(message map[string]json.RawMessage) []byte {
	return responseevents.MustMarshalJSON(struct {
		Type string                     `json:"type"`
		Item map[string]json.RawMessage `json:"item"`
	}{
		Type: responseevents.OutputItemDone,
		Item: message,
	})
}
