package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"strings"
)

type responsesEndpoint struct{}

// Source: internal/router/grok_protocol.go:339:473@413bbaa9b9202042199c753690febe7bc1358fa6 translateChatRequest
func (responsesEndpoint) encode(model string, history []providerMessage, tools []map[string]any, o providerOptions, d providerDiagnostics) (map[string]any, error) {
	body := providerRequestBody(model, o)
	body["store"] = false
	body["include"] = []string{"reasoning.encrypted_content"}
	var wireTools []any
	for _, tool := range tools {
		tool["type"] = "function"
		wireTools = append(wireTools, tool)
	}
	if len(wireTools) > 0 {
		body["tools"] = wireTools
	}
	if _, ok := o.fields["tool_choice"]; ok && len(tools) > 0 {
		if o.namedTool != "" {
			body["tool_choice"] = map[string]any{"type": "function", "name": o.namedTool}
		} else {
			body["tool_choice"] = o.choice
		}
	}
	if o.parallel != nil {
		body["parallel_tool_calls"] = o.parallel
	}
	if o.effort != "" {
		body["reasoning"] = map[string]string{"effort": o.effort}
	}
	if o.maxOutput != nil {
		body["max_output_tokens"] = o.maxOutput
	}
	switch jsonString(o.textFormat, "type") {
	case "json_object":
		body["text"] = map[string]any{"format": map[string]string{"type": "json_object"}}
	case "json_schema":
		body["text"] = map[string]any{"format": o.textFormat}
	}
	var input []any
	for _, message := range history {
		content, err := responsesContent(message.content, message.role, d)
		if err != nil {
			return nil, err
		}
		if err := rejectUnsignedReasoning(message, d); err != nil {
			return nil, err
		}
		input = append(input, message.retained...)
		if message.role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.callID, "output": content})
			continue
		}
		if len(content) > 0 {
			input = append(input, map[string]any{"type": "message", "role": message.role, "content": content})
		}
		for _, call := range message.calls {
			input = append(input, map[string]any{"type": "function_call", "call_id": call.id, "name": call.name, "arguments": call.arguments})
		}
	}
	body["input"] = input
	return body, nil
}

func (responsesEndpoint) readStream(reader io.Reader, d providerDiagnostics, send func(providerChunk) error) error {
	return normalizeResponsesStream(reader, d, send)
}

func responsesContent(value any, role string, d providerDiagnostics) ([]any, error) {
	return endpointContent(value, func(part map[string]any) (map[string]any, error) {
		switch part["type"] {
		case "text":
			kind := "input_text"
			if role == "assistant" {
				kind = "output_text"
			}
			return map[string]any{"type": kind, "text": part["text"]}, nil
		case "refusal":
			return part, nil
		case "image_url":
			image := part["image_url"].(map[string]string)
			next := map[string]any{"type": "input_image", "image_url": image["url"]}
			if detail, ok := image["detail"]; ok {
				next["detail"] = detail
			}
			return next, nil
		default:
			return nil, fmt.Errorf("unsupported %s content part", d.name)
		}
	}, d)
}

// Source: internal/router/opencode_stream.go:298:485@413bbaa9b9202042199c753690febe7bc1358fa6 normalizeResponsesStream
func normalizeResponsesStream(reader io.Reader, d providerDiagnostics, send func(providerChunk) error) error {
	texts := map[int]string{}
	refusals := map[int]bool{}
	items := map[int]jsontext.Value{}
	reasoned := map[int]bool{} // Items whose visible reasoning already streamed.
	nextTool := 0
	consumeItem := func(index int, raw jsontext.Value) error {
		if previous, ok := items[index]; ok {
			if !jsonEquivalent(previous, raw) {
				return d.streamError("invalid", fmt.Sprintf("%s changed a completed output item", d.name))
			}
			return nil
		}
		var item map[string]jsontext.Value
		if json.Unmarshal(raw, &item) != nil {
			return d.streamError("invalid", fmt.Sprintf("Invalid %s Responses item", d.name))
		}
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		switch kind {
		case "reasoning":
			// Providers populate a summary or raw text, not both; either is
			// visible when the stream carried no deltas for it.
			if !reasoned[index] {
				var parts []string
				for _, field := range []string{"summary", "content"} {
					var texts []struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(item[field], &texts)
					for _, text := range texts {
						if text.Text != "" {
							parts = append(parts, text.Text)
						}
					}
					if len(parts) > 0 {
						break
					}
				}
				if len(parts) > 0 {
					if err := send(providerChunkDelta(providerDelta{ReasoningContent: strings.Join(parts, "\n\n")})); err != nil {
						return err
					}
				}
			}
			if err := send(providerChunk{RetainedReasoning: mustMarshalJSON(item)}); err != nil {
				return err
			}
		case "function_call":
			var id, name, arguments string
			if json.Unmarshal(item["call_id"], &id) != nil || json.Unmarshal(item["name"], &name) != nil || json.Unmarshal(item["arguments"], &arguments) != nil {
				return d.streamError("invalid", fmt.Sprintf("Invalid %s function call", d.name))
			}
			if err := send(providerChunkCall(nextTool, id, name, arguments)); err != nil {
				return err
			}
			nextTool++
		case "message":
			var content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			}
			if json.Unmarshal(item["content"], &content) != nil {
				return d.streamError("invalid", fmt.Sprintf("Invalid %s message content", d.name))
			}
			var completed strings.Builder
			for _, part := range content {
				switch part.Type {
				case "output_text":
					completed.WriteString(part.Text)
				case "refusal":
					refusals[index] = true
					completed.WriteString(part.Refusal)
				default:
					return d.streamError("invalid", fmt.Sprintf("Unsupported %s output content", d.name))
				}
			}
			if texts[index] == "" {
				if err := send(providerChunkText(completed.String(), refusals[index])); err != nil {
					return err
				}
			} else if texts[index] != completed.String() {
				return d.streamError("invalid", fmt.Sprintf("%s final text differs from streamed text", d.name))
			}
		default:
			return d.streamError("invalid", fmt.Sprintf("Unsupported %s Responses output item", d.name))
		}
		items[index] = raw
		return nil
	}
	return readEndpointSSE(reader, d, func(data []byte) (bool, error) {
		var event struct {
			Type         string         `json:"type"`
			OutputIndex  int            `json:"output_index"`
			SummaryIndex int            `json:"summary_index"`
			Delta        string         `json:"delta"`
			Item         jsontext.Value `json:"item"`
			Response     struct {
				Model  string           `json:"model"`
				Output []jsontext.Value `json:"output"`
				Usage  *struct {
					Input        uint64 `json:"input_tokens"`
					Output       uint64 `json:"output_tokens"`
					InputDetails struct {
						Cached uint64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
					OutputDetails struct {
						Reasoning uint64 `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
				Incomplete struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil {
			return false, d.streamError("invalid", fmt.Sprintf("Invalid %s Responses event", d.name))
		}
		switch event.Type {
		case "response.created":
			return false, send(providerChunk{Model: event.Response.Model})
		case "response.output_text.delta", "response.refusal.delta":
			if _, completed := items[event.OutputIndex]; completed {
				return false, d.streamError("invalid", fmt.Sprintf("%s text followed a completed item", d.name))
			}
			texts[event.OutputIndex] += event.Delta
			refusal := event.Type == "response.refusal.delta"
			if refusal {
				refusals[event.OutputIndex] = true
			}
			return false, send(providerChunkText(event.Delta, refusal))
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if _, completed := items[event.OutputIndex]; completed {
				return false, d.streamError("invalid", fmt.Sprintf("%s reasoning followed a completed item", d.name))
			}
			reasoned[event.OutputIndex] = true
			return false, send(providerChunkDelta(providerDelta{ReasoningContent: event.Delta}))
		case "response.reasoning_summary_part.added":
			if event.SummaryIndex > 0 && reasoned[event.OutputIndex] {
				return false, send(providerChunkDelta(providerDelta{ReasoningContent: "\n\n"}))
			}
			return false, send(providerChunk{})
		case "response.output_item.done":
			return false, consumeItem(event.OutputIndex, event.Item)
		case "response.failed", "error":
			return false, send(providerChunk{Error: data})
		case "response.completed", "response.incomplete":
			for index, item := range event.Response.Output {
				if err := consumeItem(index, item); err != nil {
					return false, err
				}
			}
			finish := "stop"
			if nextTool > 0 {
				finish = "tool_calls"
			}
			if event.Type == "response.incomplete" {
				switch event.Response.Incomplete.Reason {
				case "max_output_tokens":
					finish = "length"
				case "content_filter":
					finish = "content_filter"
				default:
					return false, d.streamError("invalid", fmt.Sprintf("Unsupported %s incomplete reason", d.name))
				}
			}
			if event.Response.Usage != nil {
				usage := event.Response.Usage
				counts, err := endpointUsage(d, usage.Input, usage.Output, usage.InputDetails.Cached, 0, usage.OutputDetails.Reasoning)
				if err != nil {
					return false, err
				}
				if err := send(providerChunk{Model: event.Response.Model, Usage: counts}); err != nil {
					return false, err
				}
			}
			return true, send(providerChunkFinish(finish))
		case "response.in_progress", "response.output_item.added", "response.content_part.added",
			"response.content_part.done", "response.output_text.done", "response.refusal.done", "response.function_call_arguments.delta",
			"response.function_call_arguments.done", "response.reasoning_summary_part.done",
			"response.reasoning_summary_text.done", "response.reasoning_text.done":
			return false, send(providerChunk{})
		default:
			return false, d.streamError("invalid", fmt.Sprintf("Unsupported %s Responses event", d.name))
		}
	})
}
