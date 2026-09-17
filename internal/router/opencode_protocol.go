package router

import (
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
)

// Provider reasoning is opaque replay data, not an OpenAI encrypted-history
// token. Bind the envelope to its service and model so model switches fail
// explicitly instead of replaying another provider's signature.
type openCodeReasoning struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Item     jsontext.Value `json:"item"`
}

const openCodeReasoningPrefix = "mekugi-opencode-v1:"

func sealOpenCodeReasoning(service *openCodeService, model string, item any) string {
	data := mustMarshalJSON(openCodeReasoning{Provider: service.prefix, Model: model, Item: mustMarshalJSON(item)})
	return openCodeReasoningPrefix + base64.RawStdEncoding.EncodeToString(data)
}

func restoreOpenCodeReasoning(service *openCodeService, model, value string) (map[string]any, error) {
	encoded, ok := strings.CutPrefix(value, openCodeReasoningPrefix)
	if !ok {
		return nil, incompatibleRequest("opencode_encrypted_history", "OpenCode cannot read foreign encrypted reasoning; start a fresh thread or use fork_turns=none.")
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	var envelope openCodeReasoning
	if err != nil || json.Unmarshal(data, &envelope) != nil || envelope.Provider != service.prefix || envelope.Model != model {
		return nil, incompatibleRequest("opencode_encrypted_history", "OpenCode reasoning belongs to another service or model; start a fresh thread.")
	}
	var item map[string]any
	if json.Unmarshal(envelope.Item, &item) != nil || item == nil {
		return nil, errors.New("invalid retained OpenCode reasoning")
	}
	kind, _ := item["type"].(string)
	if service.format(model) == "anthropic" && (kind == "thinking" || kind == "redacted_thinking") ||
		service.format(model) == "responses" && kind == "reasoning" {
		return item, nil
	}
	return nil, errors.New("invalid retained OpenCode reasoning type")
}

// providerMessage retains validated history independently of any endpoint's
// request envelope. Only the final builder groups it into wire messages.
type providerMessage struct {
	role      string
	content   any
	callID    string
	calls     []providerCall
	retained  []any
	reasoning string
}

type providerCall struct {
	id, name, arguments string
}

func (tr *grokTranslation) writeProviderMessages(history []providerMessage) error {
	messages := []map[string]any{}
	var input, system []any
	for _, message := range history {
		role := message.role
		if tr.format == "" || tr.format == "chat" {
			wire := map[string]any{"role": role, "content": message.content}
			if role == "tool" {
				wire["tool_call_id"] = message.callID
			}
			for _, call := range message.calls {
				calls, _ := wire["tool_calls"].([]any)
				wire["tool_calls"] = append(calls, map[string]any{
					"id": call.id, "type": "function",
					"function": map[string]any{"name": call.name, "arguments": call.arguments},
				})
			}
			if message.reasoning != "" {
				wire["reasoning_content"] = message.reasoning
			}
			if parts, ok := message.content.([]any); ok {
				kept := make([]any, 0, len(parts))
				for _, part := range parts {
					fields, ok := part.(map[string]any)
					if ok && fields["type"] == "refusal" {
						if role != "assistant" {
							return errors.New("refusal history must belong to an assistant")
						}
						wire["refusal"] = fields["refusal"]
					} else {
						kept = append(kept, part)
					}
				}
				wire["content"] = kept
				if len(kept) == 0 {
					wire["content"] = nil
				}
			}
			messages = append(messages, wire)
			continue
		}

		content, err := openCodeContent(message.content, tr.format, role)
		if err != nil {
			return err
		}
		if message.reasoning != "" {
			return incompatibleRequest("opencode_encrypted_history", "Changing OpenCode API formats requires a fresh thread; unsigned reasoning cannot replace provider replay data.")
		}
		if tr.format == "anthropic" {
			if role == "system" {
				system = append(system, content...)
				continue
			}
			blocks := append(message.retained, content...)
			if role == "tool" {
				role = "user"
				blocks = []any{map[string]any{"type": "tool_result", "tool_use_id": message.callID, "content": content}}
			}
			for _, call := range message.calls {
				arguments := jsontext.Value(call.arguments)
				if arguments.Kind() != '{' {
					return errors.New("OpenCode Messages tool arguments must be JSON objects")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.id, "name": call.name, "input": arguments})
			}
			// Messages requires parallel tool results in one user message.
			if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
				last := messages[len(messages)-1]
				last["content"] = append(last["content"].([]any), blocks...)
			} else {
				messages = append(messages, map[string]any{"role": role, "content": blocks})
			}
		} else {
			input = append(input, message.retained...)
			if role == "tool" {
				input = append(input, map[string]any{"type": "function_call_output", "call_id": message.callID, "output": content})
				continue
			}
			if len(content) > 0 {
				input = append(input, map[string]any{"type": "message", "role": role, "content": content})
			}
			for _, call := range message.calls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.id, "name": call.name, "arguments": call.arguments})
			}
		}
	}
	if tr.format == "responses" {
		tr.body["input"] = input
	} else {
		tr.body["messages"] = messages
		if len(system) > 0 {
			tr.body["system"] = system
		}
	}
	return nil
}

func openCodeContent(value any, format, role string) ([]any, error) {
	var parts []any
	switch value := value.(type) {
	case nil:
		return parts, nil
	case string:
		if value != "" {
			parts = []any{map[string]any{"type": "text", "text": value}}
		}
	case []any:
		parts = value
	default:
		return nil, errors.New("unsupported OpenCode message content")
	}
	var result []any
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			text, ok := raw.(map[string]string)
			if !ok {
				return nil, errors.New("unsupported OpenCode content part")
			}
			part = map[string]any{"type": text["type"], "text": text["text"]}
		}
		switch part["type"] {
		case "text":
			kind := "text"
			if format == "responses" {
				kind = "input_text"
				if role == "assistant" {
					kind = "output_text"
				}
			}
			result = append(result, map[string]any{"type": kind, "text": part["text"]})
		case "refusal":
			if format == "responses" {
				result = append(result, part)
			} else {
				result = append(result, map[string]any{"type": "text", "text": part["refusal"]})
			}
		case "image_url":
			image := part["image_url"].(map[string]string)
			url := image["url"]
			if format == "responses" {
				next := map[string]any{"type": "input_image", "image_url": url}
				if detail, ok := image["detail"]; ok {
					next["detail"] = detail
				}
				result = append(result, next)
			} else {
				if role == "system" || role == "assistant" {
					return nil, errors.New("OpenCode Messages only supports images in user/tool input")
				}
				source := map[string]any{"type": "url", "url": url}
				if data, ok := strings.CutPrefix(url, "data:"); ok {
					media, payload, ok := strings.Cut(data, ";base64,")
					if !ok {
						return nil, errors.New("OpenCode image data must be base64 encoded")
					}
					source = map[string]any{"type": "base64", "media_type": media, "data": payload}
				}
				result = append(result, map[string]any{"type": "image", "source": source})
			}
		default:
			return nil, errors.New("unsupported OpenCode content part")
		}
	}
	return result, nil
}
