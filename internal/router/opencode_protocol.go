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

func (tr *grokTranslation) convertOpenCodeRequest() error {
	anthropic := tr.format == "anthropic"
	body := map[string]any{"model": tr.body["model"], "stream": true}
	if anthropic {
		// Messages requires a limit. This is a generation ceiling, not a claim
		// about consumption; callers may provide a smaller total-output limit.
		body["max_tokens"] = 32768
		if limit, ok := tr.body["max_output_tokens"]; ok {
			body["max_tokens"] = limit
		}
	} else {
		body["store"] = false
		body["include"] = []string{"reasoning.encrypted_content"}
		if limit, ok := tr.body["max_output_tokens"]; ok {
			body["max_output_tokens"] = limit
		}
	}
	if effort, ok := tr.body["reasoning_effort"].(string); ok {
		if anthropic {
			body["output_config"] = map[string]string{"effort": effort}
		} else {
			body["reasoning"] = map[string]string{"effort": effort}
		}
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, ok := tr.body[key]; ok {
			body[key] = value
		}
	}
	var tools []any
	for _, raw := range rangeTools(tr.body["tools"]) {
		fn := raw["function"].(map[string]any)
		tool := map[string]any{"name": fn["name"], "description": fn["description"]}
		if anthropic {
			tool["input_schema"] = fn["parameters"]
		} else {
			tool["type"] = "function"
			tool["parameters"] = fn["parameters"]
			if strict, ok := fn["strict"]; ok {
				tool["strict"] = strict
			}
		}
		tools = append(tools, tool)
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if choice, ok := tr.body["tool_choice"]; ok {
		switch choice := choice.(type) {
		case string:
			if anthropic {
				kind := choice
				if kind == "required" {
					kind = "any"
				}
				body["tool_choice"] = map[string]any{"type": kind}
			} else {
				body["tool_choice"] = choice
			}
		case map[string]any:
			name := choice["function"].(map[string]string)["name"]
			if anthropic {
				body["tool_choice"] = map[string]any{"type": "tool", "name": name}
			} else {
				body["tool_choice"] = map[string]any{"type": "function", "name": name}
			}
		}
	}
	if parallel, ok := tr.body["parallel_tool_calls"]; ok {
		if anthropic {
			var enabled bool
			if err := json.Unmarshal(mustMarshalJSON(parallel), &enabled); err != nil {
				return errors.New("invalid OpenCode parallel tool setting")
			}
			choice, _ := body["tool_choice"].(map[string]any)
			if choice == nil {
				choice = map[string]any{"type": "auto"}
			}
			choice["disable_parallel_tool_use"] = !enabled
			body["tool_choice"] = choice
		} else {
			body["parallel_tool_calls"] = parallel
		}
	}
	if format, ok := tr.body["response_format"]; ok {
		var settings map[string]any
		if err := json.Unmarshal(mustMarshalJSON(format), &settings); err != nil {
			return errors.New("invalid OpenCode output format")
		}
		if settings["type"] == "json_schema" {
			schema := settings["json_schema"].(map[string]any)
			if anthropic {
				body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema["schema"]}}
			} else {
				schema["type"] = "json_schema"
				body["text"] = map[string]any{"format": schema}
			}
		} else if anthropic {
			return errors.New("OpenCode Messages requires a JSON schema for structured output")
		} else {
			body["text"] = map[string]any{"format": settings}
		}
	}
	var messages, system []any
	for _, message := range tr.body["messages"].([]map[string]any) {
		role := message["role"].(string)
		content, err := openCodeContent(message["content"], tr.format, role)
		if err != nil {
			return err
		}
		retained, _ := message["_opencode_reasoning"].([]any)
		if reasoning, _ := message["reasoning_content"].(string); reasoning != "" {
			return incompatibleRequest("opencode_encrypted_history", "Changing OpenCode API formats requires a fresh thread; unsigned reasoning cannot replace provider replay data.")
		}
		if anthropic {
			if role == "system" {
				system = append(system, content...)
				continue
			}
			blocks := append(retained, content...)
			if role == "tool" {
				role = "user"
				blocks = []any{map[string]any{"type": "tool_result", "tool_use_id": message["tool_call_id"], "content": content}}
			}
			for _, call := range rangeTools(message["tool_calls"]) {
				fn := call["function"].(map[string]any)
				var input jsontext.Value = []byte(fn["arguments"].(string))
				if input.Kind() != '{' {
					return errors.New("OpenCode Messages tool arguments must be JSON objects")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call["id"], "name": fn["name"], "input": input})
			}
			// Anthropic requires parallel tool results in one user message.
			if len(messages) > 0 && messages[len(messages)-1].(map[string]any)["role"] == role {
				last := messages[len(messages)-1].(map[string]any)
				last["content"] = append(last["content"].([]any), blocks...)
			} else {
				messages = append(messages, map[string]any{"role": role, "content": blocks})
			}
		} else {
			messages = append(messages, retained...)
			if role == "tool" {
				messages = append(messages, map[string]any{"type": "function_call_output", "call_id": message["tool_call_id"], "output": content})
				continue
			}
			if len(content) > 0 {
				messages = append(messages, map[string]any{"type": "message", "role": role, "content": content})
			}
			for _, call := range rangeTools(message["tool_calls"]) {
				fn := call["function"].(map[string]any)
				messages = append(messages, map[string]any{"type": "function_call", "call_id": call["id"], "name": fn["name"], "arguments": fn["arguments"]})
			}
		}
	}
	if anthropic {
		body["messages"] = messages
		if len(system) > 0 {
			body["system"] = system
		}
	} else {
		body["input"] = messages
	}
	tr.body = body
	return nil
}

func rangeTools(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, item.(map[string]any))
	}
	return result
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
