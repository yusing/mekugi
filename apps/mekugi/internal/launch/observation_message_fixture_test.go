package launch

import (
	"encoding/json"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

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
