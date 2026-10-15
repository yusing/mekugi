// Source: internal/router/responses_input.go@[7a069269] carrier traversal.
package codexbinding

import (
	"encoding/json"
	"fmt"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// rewriteRequestMessages retains each projection's role and content policy.
// Unselected items and non-message input keep their provider-owned bytes.
func rewriteRequestMessages(request *binding.InstructionUpdate, topic string, transform func(responses.Item) ([]byte, bool, error)) error {
	if len(request.Input) == 0 {
		return nil
	}
	input, err := responses.DecodeInput(request.Input)
	if err != nil {
		return fmt.Errorf("decode %s: %w", topic, err)
	}
	changed := false
	for index, raw := range input.Items {
		item, ok := responses.DecodeItem(raw)
		if !ok || item.Type != "" && item.Type != "message" {
			continue
		}
		content, updated, err := transform(item)
		if err != nil {
			return err
		}
		if updated {
			item.SetContent(content)
			input.Items[index] = responses.MustMarshalJSON(item)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	encoded, err := input.Encode()
	if err != nil {
		return fmt.Errorf("encode %s: %w", topic, err)
	}
	request.Input = encoded
	request.InputChanged = true
	return nil
}

// transformResponsesTextContent transforms selected text parts in message content.
func transformResponsesTextContent(
	raw json.RawMessage,
	transform func(string) string,
	isTextPart func(string) bool,
) (json.RawMessage, bool, error) {
	if text, ok := responses.DecodeString(raw); ok {
		return responses.MustMarshalJSON(transform(text)), true, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		return raw, false, nil
	}
	changed := false
	for index, part := range parts {
		var fields struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		_ = json.Unmarshal(part, &fields)
		if fields.Text == nil || !isTextPart(fields.Type) {
			continue
		}
		updated, err := responses.ReplaceField(part, "text", responses.MustMarshalJSON(transform(*fields.Text)))
		if err != nil {
			return nil, false, err
		}
		parts[index] = updated
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := responses.MarshalJSON(parts)
	return encoded, true, err
}

func isResponsesInputTextPart(typeName string) bool {
	return typeName == "input_text" || typeName == "text"
}
