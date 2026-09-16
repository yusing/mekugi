package router

import (
	"bytes"
	"encoding/json"
	"errors"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

func (t *ctp2ResponseTransform) TransformJSON(payload []byte) ([]byte, error) {
	return t.transformJSON(payload)
}

func (t *ctp2ResponseTransform) transformJSON(payload []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil || response == nil {
		return nil, errors.New("decode CTP/2 response")
	}
	if raw, ok := response["output"]; ok {
		var output []responsesItem
		if err := json.Unmarshal(raw, &output); err != nil {
			return nil, errors.New("decode CTP/2 response output")
		}
		for index := range output {
			if err := t.transformOutputItem(&output[index]); err != nil {
				return nil, err
			}
		}
		response["output"] = mustMarshalJSON(output)
	}
	encoded, err := marshalProtocolJSON(response)
	if err != nil {
		return nil, err
	}
	if len(encoded) > upstreamJSONBufferBytes {
		return nil, errors.New("decoded CTP/2 response exceeds the router buffer budget")
	}
	return encoded, nil
}

func (t *ctp2ResponseTransform) TransformSSE(payload []byte) (transformed [][]byte, err error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil || event == nil {
		return [][]byte{payload}, nil
	}
	kind := responseevents.Kind(jsonString(event, "type"))
	switch {
	case kind.ItemEvent():
		if item, ok := decodeResponsesItem(event["item"]); ok {
			if err := t.transformOutputItem(&item); err != nil {
				return nil, err
			}
			event["item"] = mustMarshalJSON(item)
		}
	case kind == responseevents.OutputTextDone:
		compact := jsonString(event, "text")
		decoded, err := decodeCTP2String(compact, t.sources, upstreamJSONBufferBytes)
		if err != nil {
			return nil, err
		}
		event["text"] = mustMarshalJSON(decoded)
	case kind.ContentPart():
		var part map[string]json.RawMessage
		if json.Unmarshal(event["part"], &part) == nil && part != nil {
			if err := t.transformTextPart(part); err != nil {
				return nil, err
			}
			event["part"] = mustMarshalJSON(part)
		}
	}
	if rawResponse, ok := event["response"]; ok {
		trimmed := bytes.TrimSpace(rawResponse)
		if len(trimmed) != 0 && trimmed[0] == '{' {
			decoded, err := t.transformJSON(rawResponse)
			if err != nil {
				return nil, err
			}
			event["response"] = decoded
		}
	}
	encoded, err := marshalProtocolJSON(event)
	if err != nil {
		return nil, err
	}
	if len(encoded) > upstreamJSONBufferBytes {
		return nil, errors.New("decoded CTP/2 stream event exceeds the router buffer budget")
	}
	return [][]byte{encoded}, nil
}

func (t *ctp2ResponseTransform) Finish(bool) error {
	return nil
}

func (t *ctp2ResponseTransform) transformOutputItem(item *responsesItem) error {
	if item.Type != "message" || item.Role != "assistant" {
		return nil
	}
	if len(item.Content) == 0 {
		return nil
	}
	decoded, err := t.transformMessageContent(item.Content)
	if err != nil {
		return err
	}
	item.setContent(decoded)
	return nil
}

func (t *ctp2ResponseTransform) transformMessageContent(raw json.RawMessage) (json.RawMessage, error) {
	var transformErr error
	transformed, _, err := transformCTP2Content(raw, func(text string) string {
		decoded, decodeErr := decodeCTP2String(text, t.sources, upstreamJSONBufferBytes)
		if decodeErr != nil {
			transformErr = errors.Join(transformErr, decodeErr)
			return text
		}
		return decoded
	}, isCTP2AssistantTextPart)
	if err != nil {
		return nil, err
	}
	if transformErr != nil {
		return nil, transformErr
	}
	return transformed, nil
}

func (t *ctp2ResponseTransform) transformTextPart(part map[string]json.RawMessage) error {
	typeName := jsonString(part, "type")
	if typeName != "output_text" && typeName != "text" {
		return nil
	}
	compact := jsonString(part, "text")
	decoded, err := decodeCTP2String(compact, t.sources, upstreamJSONBufferBytes)
	if err != nil {
		return err
	}
	part["text"] = mustMarshalJSON(decoded)
	return nil
}
