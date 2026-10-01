package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"strings"
)

type messagesEndpoint struct{}

// Source: internal/router/grok_protocol.go:339:473@413bbaa9b9202042199c753690febe7bc1358fa6 translateChatRequest
func (messagesEndpoint) encode(model string, history []providerMessage, tools []map[string]any, o providerOptions, d providerDiagnostics) (map[string]any, error) {
	body := providerRequestBody(model, o)
	body["max_tokens"] = 32768
	var wireTools []any
	for _, tool := range tools {
		wireTools = append(wireTools, map[string]any{"name": tool["name"], "description": tool["description"], "input_schema": tool["parameters"]})
	}
	if len(wireTools) > 0 {
		body["tools"] = wireTools
	}
	if _, ok := o.fields["tool_choice"]; ok && len(tools) > 0 {
		if o.namedTool != "" {
			body["tool_choice"] = map[string]any{"type": "tool", "name": o.namedTool}
		} else {
			choice := o.choice
			if choice == "required" {
				choice = "any"
			}
			body["tool_choice"] = map[string]any{"type": choice}
		}
	}
	if o.parallel != nil {
		var enabled bool
		if json.Unmarshal(o.parallel, &enabled) != nil {
			return nil, fmt.Errorf("invalid %s parallel tool setting", d.name)
		}
		choice, _ := body["tool_choice"].(map[string]any)
		if choice == nil {
			choice = map[string]any{"type": "auto"}
		}
		choice["disable_parallel_tool_use"] = !enabled
		body["tool_choice"] = choice
	}
	outputConfig := map[string]any{}
	if o.effort != "" {
		outputConfig["effort"] = o.effort
	}
	if o.maxOutput != nil {
		body["max_tokens"] = o.maxOutput
	}
	switch jsonString(o.textFormat, "type") {
	case "json_object":
		return nil, fmt.Errorf("%s Messages requires a JSON schema for structured output", d.name)
	case "json_schema":
		outputConfig["format"] = map[string]any{"type": "json_schema", "schema": o.textFormat["schema"]}
	}
	if len(outputConfig) > 0 {
		body["output_config"] = outputConfig
	}
	messages := []map[string]any{}
	var system []any
	for _, message := range history {
		role := message.role
		content, err := messagesContent(message.content, role, d)
		if err != nil {
			return nil, err
		}
		if err := rejectUnsignedReasoning(message, d); err != nil {
			return nil, err
		}
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
				return nil, fmt.Errorf("%s Messages tool arguments must be JSON objects", d.name)
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.id, "name": call.name, "input": arguments})
		}
		// Parallel tool results share one user message in Messages.
		if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
			last := messages[len(messages)-1]
			last["content"] = append(last["content"].([]any), blocks...)
		} else {
			messages = append(messages, map[string]any{"role": role, "content": blocks})
		}
	}
	body["messages"] = messages
	if len(system) > 0 {
		body["system"] = system
	}
	return body, nil
}

func (messagesEndpoint) readStream(reader io.Reader, d providerDiagnostics, send func(providerChunk) error) error {
	return normalizeAnthropicStream(reader, d, send)
}

func messagesContent(value any, role string, d providerDiagnostics) ([]any, error) {
	return endpointContent(value, func(part map[string]any) (map[string]any, error) {
		switch part["type"] {
		case "text":
			return map[string]any{"type": "text", "text": part["text"]}, nil
		case "refusal":
			return map[string]any{"type": "text", "text": part["refusal"]}, nil
		case "image_url":
			if role == "system" || role == "assistant" {
				return nil, fmt.Errorf("%s Messages only supports images in user/tool input", d.name)
			}
			image := part["image_url"].(map[string]string)
			url := image["url"]
			source := map[string]any{"type": "url", "url": url}
			if data, ok := strings.CutPrefix(url, "data:"); ok {
				media, payload, ok := strings.Cut(data, ";base64,")
				if !ok {
					return nil, fmt.Errorf("%s image data must be base64 encoded", d.name)
				}
				source = map[string]any{"type": "base64", "media_type": media, "data": payload}
			}
			return map[string]any{"type": "image", "source": source}, nil
		default:
			return nil, fmt.Errorf("unsupported %s content part", d.name)
		}
	}, d)
}

type anthropicStreamBlock struct {
	raw       map[string]jsontext.Value
	kind      string
	text      strings.Builder
	signature strings.Builder
	arguments strings.Builder
}

// Source: internal/router/opencode_stream.go:117:296@413bbaa9b9202042199c753690febe7bc1358fa6 normalizeAnthropicStream
func normalizeAnthropicStream(reader io.Reader, d providerDiagnostics, send func(providerChunk) error) error {
	blocks := map[int]*anthropicStreamBlock{}
	started := false
	nextBlock, nextTool := 0, 0
	finish := ""
	usage := map[string]jsontext.Value{}
	mergeUsage := func(raw jsontext.Value) error {
		if len(raw) == 0 {
			return nil
		}
		var update map[string]jsontext.Value
		if json.Unmarshal(raw, &update) != nil {
			return d.streamError("invalid", fmt.Sprintf("Invalid %s Messages usage", d.name))
		}
		maps.Copy(usage, update)
		return nil
	}
	return readEndpointSSE(reader, d, func(data []byte) (bool, error) {
		var event struct {
			Type         string                    `json:"type"`
			Index        int                       `json:"index"`
			ContentBlock map[string]jsontext.Value `json:"content_block"`
			Delta        map[string]jsontext.Value `json:"delta"`
			Usage        jsontext.Value            `json:"usage"`
			Message      struct {
				Model string         `json:"model"`
				Usage jsontext.Value `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(data, &event) != nil {
			return false, d.streamError("invalid", fmt.Sprintf("Invalid %s Messages event", d.name))
		}
		textField := func(fields map[string]jsontext.Value, key string) string {
			var value string
			_ = json.Unmarshal(fields[key], &value)
			return value
		}
		if event.Type == "ping" {
			return false, nil
		}
		if event.Type == "error" {
			return false, send(providerChunk{Error: data})
		}
		if event.Type == "message_start" {
			if started {
				return false, d.streamError("invalid", fmt.Sprintf("Duplicate %s message start", d.name))
			}
			started = true
			if err := mergeUsage(event.Message.Usage); err != nil {
				return false, err
			}
			return false, send(providerChunk{Model: event.Message.Model})
		}
		if !started || (finish != "" && event.Type != "message_stop") {
			return false, d.streamError("invalid", fmt.Sprintf("%s Messages event outside an active message", d.name))
		}
		switch event.Type {
		case "content_block_start":
			if event.Index != nextBlock || len(event.ContentBlock) == 0 {
				return false, d.streamError("invalid", fmt.Sprintf("Invalid %s content-block index", d.name))
			}
			nextBlock++
			block := &anthropicStreamBlock{raw: event.ContentBlock, kind: textField(event.ContentBlock, "type")}
			blocks[event.Index] = block
			switch block.kind {
			case "text":
				text := textField(block.raw, "text")
				block.text.WriteString(text)
				if text != "" {
					return false, send(providerChunkDelta(providerDelta{Content: text}))
				}
			case "thinking":
				thinking := textField(block.raw, "thinking")
				block.text.WriteString(thinking)
				block.signature.WriteString(textField(block.raw, "signature"))
				if thinking != "" {
					return false, send(providerChunkDelta(providerDelta{ReasoningContent: thinking}))
				}
			case "redacted_thinking", "tool_use":
			default:
				return false, d.streamError("invalid", fmt.Sprintf("Unsupported %s content block", d.name))
			}
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return false, d.streamError("invalid", fmt.Sprintf("%s delta has no active content block", d.name))
			}
			kind := textField(event.Delta, "type")
			switch {
			case kind == "text_delta" && block.kind == "text":
				text := textField(event.Delta, "text")
				block.text.WriteString(text)
				return false, send(providerChunkDelta(providerDelta{Content: text}))
			case kind == "thinking_delta" && block.kind == "thinking":
				thinking := textField(event.Delta, "thinking")
				block.text.WriteString(thinking)
				return false, send(providerChunkDelta(providerDelta{ReasoningContent: thinking}))
			case kind == "signature_delta" && block.kind == "thinking":
				block.signature.WriteString(textField(event.Delta, "signature"))
			case kind == "input_json_delta" && block.kind == "tool_use":
				block.arguments.WriteString(textField(event.Delta, "partial_json"))
			default:
				return false, d.streamError("invalid", fmt.Sprintf("Unsupported %s content delta", d.name))
			}
		case "content_block_stop":
			block := blocks[event.Index]
			if block == nil {
				return false, d.streamError("invalid", fmt.Sprintf("%s stopped an unknown content block", d.name))
			}
			delete(blocks, event.Index)
			switch block.kind {
			case "thinking", "redacted_thinking":
				if block.kind == "thinking" {
					block.raw["thinking"] = mustMarshalJSON(block.text.String())
					if block.signature.Len() > 0 {
						block.raw["signature"] = mustMarshalJSON(block.signature.String())
					}
				}
				return false, send(providerChunk{RetainedReasoning: mustMarshalJSON(block.raw)})
			case "tool_use":
				arguments := block.arguments.String()
				if arguments == "" {
					arguments = string(block.raw["input"])
				}
				chunk := providerChunkCall(nextTool, textField(block.raw, "id"), textField(block.raw, "name"), arguments)
				nextTool++
				return false, send(chunk)
			}
		case "message_delta":
			if len(blocks) != 0 {
				return false, d.streamError("invalid", fmt.Sprintf("%s ended a message with unfinished content", d.name))
			}
			if err := mergeUsage(event.Usage); err != nil {
				return false, err
			}
			switch textField(event.Delta, "stop_reason") {
			case "end_turn", "stop_sequence":
				finish = "stop"
			case "tool_use":
				finish = "tool_calls"
			case "max_tokens":
				finish = "length"
			case "refusal":
				finish = "content_filter"
			default:
				return false, d.streamError("invalid", fmt.Sprintf("Unsupported %s stop reason", d.name))
			}
		case "message_stop":
			if finish == "" || len(blocks) != 0 {
				return false, d.streamError("invalid", fmt.Sprintf("%s message stopped before completion", d.name))
			}
			count := func(key string) (uint64, bool) {
				var value uint64
				err := json.Unmarshal(usage[key], &value)
				return value, err == nil
			}
			input, inputOK := count("input_tokens")
			output, outputOK := count("output_tokens")
			cached, _ := count("cache_read_input_tokens")
			written, _ := count("cache_creation_input_tokens")
			if inputOK && outputOK {
				if ^uint64(0)-input < cached || ^uint64(0)-input-cached < written {
					return false, d.streamError("invalid", fmt.Sprintf("Invalid %s token counts", d.name))
				}
				counts, err := endpointUsage(d, input+cached+written, output, cached, written, 0)
				if err != nil {
					return false, err
				}
				if err := send(providerChunk{Usage: counts}); err != nil {
					return false, err
				}
			}
			return true, send(providerChunkFinish(finish))
		default:
			return false, d.streamError("invalid", fmt.Sprintf("Unsupported %s Messages event", d.name))
		}
		// Non-text progress still reaches the shared stream reader.
		return false, send(providerChunk{})
	})
}
