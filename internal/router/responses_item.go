package router

import (
	"bytes"
	"encoding/json"

	"maps"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

// responsesItem keeps the complete provider object while exposing the stable
// fields used by exec, commentary, history, and carrier rendering.
type responsesItem struct {
	fields    map[string]json.RawMessage
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

// decodeResponsesItem decodes a Responses item from raw JSON.
func decodeResponsesItem(raw json.RawMessage) (responsesItem, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return responsesItem{}, false
	}
	return newResponsesItem(fields), true
}

// newResponsesItem creates a responsesItem from decoded JSON fields.
func newResponsesItem(fields map[string]json.RawMessage) responsesItem {
	item := responsesItem{
		fields:    fields,
		Type:      responseevents.StringField(fields, "type"),
		Role:      responseevents.StringField(fields, "role"),
		ID:        responseevents.StringField(fields, "id"),
		CallID:    responseevents.StringField(fields, "call_id"),
		Name:      responseevents.StringField(fields, "name"),
		Namespace: responseevents.StringField(fields, "namespace"),
		Status:    responseevents.StringField(fields, "status"),
		Content:   fields["content"],
		Output:    fields["output"],
	}
	if value, ok := decodeJSONString(fields["input"]); ok {
		item.Input = new(value)
	}
	if value, ok := decodeJSONString(fields["arguments"]); ok {
		item.Arguments = new(value)
	}
	return item
}

func (item responsesItem) MarshalJSON() ([]byte, error) {
	if item.fields == nil {
		return []byte("null"), nil
	}
	return responseevents.MarshalJSON(item.fields)
}

func (item *responsesItem) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*item = newResponsesItem(fields)
	return nil
}

// setContent updates the item's content field.
func (item *responsesItem) setContent(content json.RawMessage) {
	item.Content = content
	item.fields["content"] = content
}

// setInput updates the item's input field.
func (item *responsesItem) setInput(input string) {
	item.Input = new(input)
	item.fields["input"] = responseevents.MustMarshalJSON(input)
}

// cloneFields returns a shallow clone of the item's JSON field map.
func (item responsesItem) cloneFields() map[string]json.RawMessage {
	return maps.Clone(item.fields)
}

// decodeJSONString decodes a JSON string value from raw JSON.
func decodeJSONString(raw json.RawMessage) (string, bool) {
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

func responseMessageFacts(fields map[string]json.RawMessage) responseevents.MessageFacts {
	return responseevents.MessageFacts{
		Kind:  responseevents.ItemKind(responseevents.StringField(fields, "type")),
		Role:  responseevents.StringField(fields, "role"),
		Phase: responseevents.StringField(fields, "phase"),
	}
}
