package router

import (
	"bufio"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"strings"
)

// Normalize endpoint-specific framing into the existing Chat stream validator.
// This preserves one owner for tool validation and Responses lifecycle emission.
func (tr *grokTranslation) readProviderStream(upstream io.ReadCloser, emit func(map[string]any) error) (map[string]any, error) {
	if tr.openCode == nil || tr.format == "chat" {
		return tr.readGrokStream(upstream, emit)
	}
	reader, writer := io.Pipe()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		send := func(chunk map[string]any) error {
			_, err := fmt.Fprintf(writer, "data: %s\n\n", mustMarshalJSON(chunk))
			return err
		}
		var err error
		if tr.format == "anthropic" {
			err = normalizeAnthropicStream(upstream, send)
		} else {
			err = normalizeResponsesStream(upstream, send)
		}
		if err == nil {
			_, err = io.WriteString(writer, "data: [DONE]\n\n")
		}
		writer.CloseWithError(err)
	}()
	result, err := tr.readGrokStream(reader, emit)
	reader.Close()
	// Also release a normalizer blocked on provider input if validation or the
	// downstream writer failed. Closing only the pipe cannot unblock that read.
	if err != nil {
		upstream.Close()
	}
	<-finished
	return result, err
}

func openCodeStreamError(message string) error {
	return staticCriticalDiagnostic("opencode_stream_invalid", message)
}

func readOpenCodeSSE(reader io.Reader, consume func([]byte) (bool, error)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), upstreamJSONBufferBytes)
	var data []string
	budget := 0
	flush := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := []byte(strings.Join(data, "\n"))
		data = nil
		return consume(payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		budget += len(line)
		if budget > upstreamJSONBufferBytes {
			return openCodeStreamError("OpenCode response exceeds the router buffer budget")
		}
		if line == "" {
			done, err := flush()
			if err != nil || done {
				return err
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return forwardCriticalDiagnostic(err)
	}
	if done, err := flush(); err != nil || done {
		return err
	}
	return openCodeStreamError("OpenCode stream ended without its terminal event")
}

func openCodeDelta(delta map[string]any) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}}
}

func openCodeFinish(reason string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": reason}}}
}

type anthropicStreamBlock struct {
	raw       map[string]jsontext.Value
	kind      string
	text      strings.Builder
	signature strings.Builder
	arguments strings.Builder
}

func normalizeAnthropicStream(reader io.Reader, send func(map[string]any) error) error {
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
			return openCodeStreamError("Invalid OpenCode Messages usage")
		}
		for key, value := range update {
			usage[key] = value
		}
		return nil
	}
	return readOpenCodeSSE(reader, func(data []byte) (bool, error) {
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
			return false, openCodeStreamError("Invalid OpenCode Messages event")
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
			return false, openCodeStreamError("OpenCode Messages reported a provider error")
		}
		if event.Type == "message_start" {
			if started {
				return false, openCodeStreamError("Duplicate OpenCode message start")
			}
			started = true
			if err := mergeUsage(event.Message.Usage); err != nil {
				return false, err
			}
			return false, send(map[string]any{"model": event.Message.Model})
		}
		if !started || (finish != "" && event.Type != "message_stop") {
			return false, openCodeStreamError("OpenCode Messages event outside an active message")
		}
		switch event.Type {
		case "content_block_start":
			if event.Index != nextBlock || len(event.ContentBlock) == 0 {
				return false, openCodeStreamError("Invalid OpenCode content-block index")
			}
			nextBlock++
			block := &anthropicStreamBlock{raw: event.ContentBlock, kind: textField(event.ContentBlock, "type")}
			blocks[event.Index] = block
			switch block.kind {
			case "text":
				text := textField(block.raw, "text")
				block.text.WriteString(text)
				if text != "" {
					return false, send(openCodeDelta(map[string]any{"content": text}))
				}
			case "thinking":
				block.text.WriteString(textField(block.raw, "thinking"))
				block.signature.WriteString(textField(block.raw, "signature"))
			case "redacted_thinking", "tool_use":
			default:
				return false, openCodeStreamError("Unsupported OpenCode content block")
			}
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return false, openCodeStreamError("OpenCode delta has no active content block")
			}
			kind := textField(event.Delta, "type")
			switch {
			case kind == "text_delta" && block.kind == "text":
				text := textField(event.Delta, "text")
				block.text.WriteString(text)
				return false, send(openCodeDelta(map[string]any{"content": text}))
			case kind == "thinking_delta" && block.kind == "thinking":
				block.text.WriteString(textField(event.Delta, "thinking"))
			case kind == "signature_delta" && block.kind == "thinking":
				block.signature.WriteString(textField(event.Delta, "signature"))
			case kind == "input_json_delta" && block.kind == "tool_use":
				block.arguments.WriteString(textField(event.Delta, "partial_json"))
			default:
				return false, openCodeStreamError("Unsupported OpenCode content delta")
			}
		case "content_block_stop":
			block := blocks[event.Index]
			if block == nil {
				return false, openCodeStreamError("OpenCode stopped an unknown content block")
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
				return false, send(map[string]any{"_mekugi_reasoning": block.raw})
			case "tool_use":
				arguments := block.arguments.String()
				if arguments == "" {
					arguments = string(block.raw["input"])
				}
				chunk := openCodeDelta(map[string]any{"tool_calls": []any{map[string]any{
					"index": nextTool, "id": textField(block.raw, "id"),
					"function": map[string]any{"name": textField(block.raw, "name"), "arguments": arguments},
				}}})
				nextTool++
				return false, send(chunk)
			}
		case "message_delta":
			if len(blocks) != 0 {
				return false, openCodeStreamError("OpenCode ended a message with unfinished content")
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
				return false, openCodeStreamError("Unsupported OpenCode stop reason")
			}
		case "message_stop":
			if finish == "" || len(blocks) != 0 {
				return false, openCodeStreamError("OpenCode message stopped before completion")
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
					return false, openCodeStreamError("Invalid OpenCode token counts")
				}
				if err := send(map[string]any{"usage": map[string]any{
					"prompt_tokens": input + cached + written, "completion_tokens": output,
					"prompt_tokens_details": map[string]any{"cached_tokens": cached, "cache_write_tokens": written},
				}}); err != nil {
					return false, err
				}
			}
			return true, send(openCodeFinish(finish))
		default:
			return false, openCodeStreamError("Unsupported OpenCode Messages event")
		}
		// Non-text progress still reaches the shared stream reader.
		return false, send(map[string]any{})
	})
}

func normalizeResponsesStream(reader io.Reader, send func(map[string]any) error) error {
	texts := map[int]string{}
	refusals := map[int]bool{}
	items := map[int]jsontext.Value{}
	nextTool := 0
	consumeItem := func(index int, raw jsontext.Value) error {
		if previous, ok := items[index]; ok {
			if !jsonEquivalent(previous, raw) {
				return openCodeStreamError("OpenCode changed a completed output item")
			}
			return nil
		}
		var item map[string]jsontext.Value
		if json.Unmarshal(raw, &item) != nil {
			return openCodeStreamError("Invalid OpenCode Responses item")
		}
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		switch kind {
		case "reasoning":
			if err := send(map[string]any{"_mekugi_reasoning": item}); err != nil {
				return err
			}
		case "function_call":
			var id, name, arguments string
			if json.Unmarshal(item["call_id"], &id) != nil || json.Unmarshal(item["name"], &name) != nil || json.Unmarshal(item["arguments"], &arguments) != nil {
				return openCodeStreamError("Invalid OpenCode function call")
			}
			if err := send(openCodeDelta(map[string]any{"tool_calls": []any{map[string]any{
				"index": nextTool, "id": id, "function": map[string]any{"name": name, "arguments": arguments},
			}}})); err != nil {
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
				return openCodeStreamError("Invalid OpenCode message content")
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
					return openCodeStreamError("Unsupported OpenCode output content")
				}
			}
			if texts[index] == "" {
				key := "content"
				if refusals[index] {
					key = "refusal"
				}
				if err := send(openCodeDelta(map[string]any{key: completed.String()})); err != nil {
					return err
				}
			} else if texts[index] != completed.String() {
				return openCodeStreamError("OpenCode final text differs from streamed text")
			}
		default:
			return openCodeStreamError("Unsupported OpenCode Responses output item")
		}
		items[index] = raw
		return nil
	}
	return readOpenCodeSSE(reader, func(data []byte) (bool, error) {
		var event struct {
			Type        string         `json:"type"`
			OutputIndex int            `json:"output_index"`
			Delta       string         `json:"delta"`
			Item        jsontext.Value `json:"item"`
			Response    struct {
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
			return false, openCodeStreamError("Invalid OpenCode Responses event")
		}
		switch event.Type {
		case "response.created":
			return false, send(map[string]any{"model": event.Response.Model})
		case "response.output_text.delta", "response.refusal.delta":
			if _, completed := items[event.OutputIndex]; completed {
				return false, openCodeStreamError("OpenCode text followed a completed item")
			}
			texts[event.OutputIndex] += event.Delta
			key := "content"
			if event.Type == "response.refusal.delta" {
				refusals[event.OutputIndex] = true
				key = "refusal"
			}
			return false, send(openCodeDelta(map[string]any{key: event.Delta}))
		case "response.output_item.done":
			return false, consumeItem(event.OutputIndex, event.Item)
		case "response.failed", "error":
			return false, openCodeStreamError("OpenCode Responses reported a provider error")
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
					return false, openCodeStreamError("Unsupported OpenCode incomplete reason")
				}
			}
			if event.Response.Usage != nil {
				usage := event.Response.Usage
				if err := send(map[string]any{"model": event.Response.Model, "usage": map[string]any{
					"prompt_tokens": usage.Input, "completion_tokens": usage.Output,
					"prompt_tokens_details":     map[string]any{"cached_tokens": usage.InputDetails.Cached},
					"completion_tokens_details": map[string]any{"reasoning_tokens": usage.OutputDetails.Reasoning},
				}}); err != nil {
					return false, err
				}
			}
			return true, send(openCodeFinish(finish))
		case "response.in_progress", "response.output_item.added", "response.content_part.added",
			"response.content_part.done", "response.output_text.done", "response.refusal.done", "response.function_call_arguments.delta",
			"response.function_call_arguments.done", "response.reasoning_summary_part.added",
			"response.reasoning_summary_part.done", "response.reasoning_summary_text.delta",
			"response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning_text.done":
			return false, send(map[string]any{})
		default:
			return false, openCodeStreamError("Unsupported OpenCode Responses event")
		}
	})
}

func jsonEquivalent(left, right jsontext.Value) bool {
	left, right = append(jsontext.Value(nil), left...), append(jsontext.Value(nil), right...)
	if left.Canonicalize() != nil || right.Canonicalize() != nil {
		return false
	}
	return string(left) == string(right)
}
