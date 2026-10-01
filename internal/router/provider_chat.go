package router

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type chatEndpoint struct{}

func (chatEndpoint) encode(model string, history []providerMessage, tools []map[string]any, o providerOptions, d providerDiagnostics) (map[string]any, error) {
	body := providerRequestBody(model, o)
	body["stream_options"] = map[string]any{"include_usage": true}
	var wireTools []any
	for _, tool := range tools {
		wireTools = append(wireTools, map[string]any{"type": "function", "function": tool})
	}
	if len(wireTools) > 0 {
		body["tools"] = wireTools
	}
	if _, ok := o.fields["tool_choice"]; ok && len(tools) > 0 {
		if o.namedTool != "" {
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": o.namedTool}}
		} else {
			body["tool_choice"] = o.choice
		}
	}
	if o.parallel != nil {
		body["parallel_tool_calls"] = o.parallel
	}
	if o.effort != "" {
		body["reasoning_effort"] = o.effort
	}
	if o.maxOutput != nil {
		return nil, fmt.Errorf("%s Chat Completions cannot enforce max_output_tokens including reasoning; omit this unsupported setting", d.subject)
	}
	switch jsonString(o.textFormat, "type") {
	case "json_object":
		body["response_format"] = map[string]string{"type": "json_object"}
	case "json_schema":
		delete(o.textFormat, "type")
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": o.textFormat}
	}
	messages, err := chatMessages(history)
	if err != nil {
		return nil, err
	}
	body["messages"] = messages
	return body, nil
}

func (chatEndpoint) readStream(reader io.Reader, d providerDiagnostics, send func(providerChunk) error) error {
	return readChatChunks(reader, d, send)
}

// Source: internal/router/opencode_protocol.go:64:104@413bbaa9b9202042199c753690febe7bc1358fa6 writeProviderMessages
func chatMessages(history []providerMessage) ([]map[string]any, error) {
	messages := []map[string]any{}
	for _, message := range history {
		role := message.role
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
						return nil, errors.New("refusal history must belong to an assistant")
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
	}
	return messages, nil
}

func readChatChunks(reader io.Reader, d providerDiagnostics, consume func(providerChunk) error) error {
	done := false
	decode := func(data string) error {
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk providerChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return d.streamError("invalid_json", fmt.Sprintf("invalid JSON in %s response stream", d.name))
		}
		return consume(chunk)
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), upstreamJSONBufferBytes)
	var data []string
	budget := 0
	for scanner.Scan() {
		line := scanner.Text()
		budget += len(line)
		if budget > upstreamJSONBufferBytes {
			return d.streamError("buffer_budget", fmt.Sprintf("%s response exceeds the router buffer budget", d.name))
		}
		if line == "" {
			if len(data) > 0 {
				if err := decode(strings.Join(data, "\n")); err != nil {
					return err
				}
				data = nil
			}
			if done {
				break
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s stream: %w", d.name, forwardCriticalDiagnostic(err))
	}
	if !done && len(data) > 0 {
		if err := decode(strings.Join(data, "\n")); err != nil {
			return err
		}
	}
	if !done {
		return d.streamError("missing_done", fmt.Sprintf("%s stream ended without [DONE]", d.name))
	}
	return nil
}
