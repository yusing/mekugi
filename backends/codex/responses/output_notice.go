package responses

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: apps/mekugi/internal/router/mekugi_history.go:365:381@874a6e86.
func AppendOutputNotice(raw jsonv1.RawMessage, warning string) (jsonv1.RawMessage, bool, error) {
	var parts []jsonv1.RawMessage
	var text string
	if json.Unmarshal(raw, &text, jsonv1.DefaultOptionsV1()) == nil {
		parts = []jsonv1.RawMessage{responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": text})}
	} else if err := json.Unmarshal(raw, &parts, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, false, errors.New("tool output must be text or content parts")
	}
	notice := responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": warning})
	for _, part := range parts {
		if responses.SameJSONValue(part, notice) {
			return raw, false, nil
		}
	}
	return responses.MustMarshalJSON(append(parts, notice)), true, nil
}
