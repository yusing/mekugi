// Source: internal/router/responses_item.go:1:113@[7a069269] raw item views.
package responses

import (
	"bytes"
	"encoding/json"

	"maps"
)

// Item keeps the complete provider object while exposing the stable
// fields used by exec, commentary, history, and carrier rendering.
type Item struct {
	Fields    map[string]json.RawMessage
	Type      string
	Role      string
	ID        string
	CallID    string
	Name      string
	Namespace string
	Status    string
	Content   json.RawMessage
	Output    json.RawMessage
	Input     *string
	Arguments *string
}

// DecodeItem decodes a Responses item from raw JSON.
func DecodeItem(raw json.RawMessage) (Item, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Item{}, false
	}
	return NewItem(fields), true
}

// NewItem creates an Item from decoded JSON fields.
func NewItem(fields map[string]json.RawMessage) Item {
	item := Item{
		Fields:    fields,
		Type:      StringField(fields, "type"),
		Role:      StringField(fields, "role"),
		ID:        StringField(fields, "id"),
		CallID:    StringField(fields, "call_id"),
		Name:      StringField(fields, "name"),
		Namespace: StringField(fields, "namespace"),
		Status:    StringField(fields, "status"),
		Content:   fields["content"],
		Output:    fields["output"],
	}
	if value, ok := DecodeString(fields["input"]); ok {
		item.Input = new(value)
	}
	if value, ok := DecodeString(fields["arguments"]); ok {
		item.Arguments = new(value)
	}
	return item
}

func (item Item) MarshalJSON() ([]byte, error) {
	if item.Fields == nil {
		return []byte("null"), nil
	}
	return MarshalJSON(item.Fields)
}

func (item *Item) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*item = NewItem(fields)
	return nil
}

// SetContent updates the item's content field.
func (item *Item) SetContent(content json.RawMessage) {
	item.Content = content
	item.Fields["content"] = content
}

// SetInput updates the item's input field.
func (item *Item) SetInput(input string) {
	item.Input = new(input)
	item.Fields["input"] = MustMarshalJSON(input)
}

// CloneFields returns a shallow clone of the item's JSON field map.
func (item Item) CloneFields() map[string]json.RawMessage {
	return maps.Clone(item.Fields)
}

// DecodeString decodes a JSON string value from raw JSON.
func DecodeString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '"' && trimmed[0] != 'n') {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func Facts(fields map[string]json.RawMessage) MessageFacts {
	return MessageFacts{
		Kind:  ItemKind(StringField(fields, "type")),
		Role:  StringField(fields, "role"),
		Phase: StringField(fields, "phase"),
	}
}
