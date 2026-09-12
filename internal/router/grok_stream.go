package router

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
)

type grokChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		ReasoningTokens  int64 `json:"reasoning_tokens"`
		PromptDetails    struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}
type grokStreamCall struct {
	id              string
	name, arguments strings.Builder
}

// readGrokStream produces ordinary Responses events. Tool arguments are held
// until complete and validated, while text and content-free progress stream
// immediately. EOF without [DONE] is never a successful completion.
func (tr *grokTranslation) readGrokStream(reader io.Reader, emit func(map[string]any) error) (map[string]any, error) {
	id := "resp_" + rand.Text()
	messageID := "msg_" + rand.Text()
	model := "grok-4.6"
	text := strings.Builder{}
	calls := map[int]*grokStreamCall{}
	finish := ""
	var usage any
	output := []any{}
	messageStarted := false
	done := false
	response := func(status string) map[string]any {
		r := map[string]any{"id": id, "object": "response", "status": status, "model": model, "output": output}
		if usage != nil {
			r["usage"] = usage
		}
		return r
	}
	if err := emit(map[string]any{"type": "response.created", "response": response("in_progress")}); err != nil {
		return nil, err
	}
	consume := func(data string) error {
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk grokChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return errors.New("invalid JSON in Grok response stream")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return errors.New("Grok reported a streaming error")
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			u := chunk.Usage
			if u.CompletionDetails.ReasoningTokens == 0 {
				u.CompletionDetails.ReasoningTokens = u.ReasoningTokens
			}
			// xAI Chat counts reasoning separately from completion tokens; Responses includes it in output.
			outputTokens := u.CompletionTokens + u.CompletionDetails.ReasoningTokens
			usage = map[string]any{"input_tokens": u.PromptTokens, "output_tokens": outputTokens, "total_tokens": u.PromptTokens + outputTokens, "input_tokens_details": map[string]any{"cached_tokens": u.PromptDetails.CachedTokens}, "output_tokens_details": map[string]any{"reasoning_tokens": u.CompletionDetails.ReasoningTokens}}
		}
		textEmitted := false
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return errors.New("Grok returned multiple completion choices")
			}
			// A terminal choice seals its data, including incomplete calls.
			// Usage-only trailers have no choices and remain admissible.
			if finish != "" {
				return errors.New("Grok returned choice data after its terminal finish reason")
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			if value := choice.Delta.Content; value != "" {
				if !messageStarted {
					messageStarted = true
					item := map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "in_progress", "phase": "final_answer", "content": []any{}}
					if err := emit(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}); err != nil {
						return err
					}
					if err := emit(map[string]any{"type": "response.content_part.added", "item_id": messageID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}); err != nil {
						return err
					}
				}
				text.WriteString(value)
				textEmitted = true
				if err := emit(map[string]any{"type": "response.output_text.delta", "item_id": messageID, "output_index": 0, "content_index": 0, "delta": value}); err != nil {
					return err
				}
			}
			for _, part := range choice.Delta.ToolCalls {
				if part.Index < 0 || part.Index > 1023 {
					return errors.New("Grok tool-call index exceeds the response budget")
				}
				call := calls[part.Index]
				if call == nil {
					call = &grokStreamCall{}
					calls[part.Index] = call
				}
				if part.ID != "" {
					if call.id != "" && call.id != part.ID {
						return errors.New("Grok changed a tool-call identity")
					}
					call.id = part.ID
				}
				call.name.WriteString(part.Function.Name)
				call.arguments.WriteString(part.Function.Arguments)
			}
		}
		if !textEmitted {
			return emit(map[string]any{"type": "response.in_progress", "response": map[string]any{"id": id, "status": "in_progress"}})
		}
		return nil
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), upstreamJSONBufferBytes)
	var data []string
	budget := 0
	for scanner.Scan() {
		line := scanner.Text()
		budget += len(line)
		if budget > upstreamJSONBufferBytes {
			return nil, errors.New("Grok response exceeds the router buffer budget")
		}
		if line == "" {
			if len(data) > 0 {
				if err := consume(strings.Join(data, "\n")); err != nil {
					return nil, err
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
		return nil, fmt.Errorf("read Grok stream: %w", err)
	}
	if !done && len(data) > 0 {
		if err := consume(strings.Join(data, "\n")); err != nil {
			return nil, err
		}
	}
	if !done {
		return nil, errors.New("Grok stream ended without [DONE]")
	}
	switch finish {
	case "stop", "tool_calls", "length", "content_filter":
	default:
		return nil, errors.New("Grok stream has no supported terminal finish reason")
	}
	if finish == "tool_calls" && len(calls) == 0 {
		return nil, errors.New("Grok finished tool calls without a call")
	}
	if len(calls) > 0 && finish != "tool_calls" {
		return nil, errors.New("Grok tool arguments were not completed")
	}
	if messageStarted {
		part := map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}
		item := map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{part}}
		if len(calls) > 0 {
			item["phase"] = "commentary"
		}
		output = append(output, item)
		for _, event := range []map[string]any{
			{"type": "response.output_text.done", "item_id": messageID, "output_index": 0, "content_index": 0, "text": text.String()},
			{"type": "response.content_part.done", "item_id": messageID, "output_index": 0, "content_index": 0, "part": part},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
		} {
			if err := emit(event); err != nil {
				return nil, err
			}
		}
	}
	// Validate all parallel calls before exposing any executable item.
	var callItems []map[string]any
	seen := map[string]bool{}
	for index := 0; index < len(calls); index++ {
		call := calls[index]
		if call == nil {
			return nil, errors.New("Grok returned noncontiguous tool-call indexes")
		}
		tool, ok := tr.tools[call.name.String()]
		if !ok {
			return nil, errors.New("Grok returned an unavailable tool")
		}
		if call.id == "" || seen[call.id] {
			return nil, errors.New("Grok returned an invalid tool-call identity")
		}
		seen[call.id] = true
		arguments := call.arguments.String()
		if !json.Valid([]byte(arguments)) {
			return nil, errors.New("Grok returned invalid tool-call arguments")
		}
		item := map[string]any{"type": "function_call", "id": "fc_" + rand.Text(), "call_id": call.id, "name": tool.name, "arguments": arguments, "status": "completed"}
		if tool.namespace != "" {
			item["namespace"] = tool.namespace
		}
		if tool.kind == "custom" {
			var args map[string]json.RawMessage
			if json.Unmarshal([]byte(arguments), &args) != nil || len(args) != 1 {
				return nil, errors.New("Grok custom tool requires exactly one input string")
			}
			var input *string
			if json.Unmarshal(args["input"], &input) != nil || input == nil {
				return nil, errors.New("Grok custom tool input is not a string")
			}
			delete(item, "arguments")
			item["type"] = "custom_tool_call"
			item["input"] = *input
		}
		callItems = append(callItems, item)
	}
	for _, item := range callItems {
		index := len(output)
		output = append(output, item)
		// Emit the complete Responses tool lifecycle only after every call has
		// validated. Router transforms use input/arguments.done as the handoff
		// boundary, even though Chat Completions buffers the whole call.
		field, doneType := "arguments", "response.function_call_arguments.done"
		if item["type"] == "custom_tool_call" {
			field, doneType = "input", "response.custom_tool_call_input.done"
		}
		added := maps.Clone(item)
		added["status"] = "in_progress"
		added[field] = ""
		if err := emit(map[string]any{"type": "response.output_item.added", "output_index": index, "item": added}); err != nil {
			return nil, err
		}
		doneEvent := map[string]any{"type": doneType, "output_index": index, "item_id": item["id"], "call_id": item["call_id"], field: item[field]}
		if item["type"] == "function_call" {
			doneEvent["name"] = item["name"]
		}
		if err := emit(doneEvent); err != nil {
			return nil, err
		}
		if err := emit(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item}); err != nil {
			return nil, err
		}
	}
	status := "completed"
	if finish == "length" || finish == "content_filter" {
		status = "incomplete"
	}
	result := response(status)
	if status == "incomplete" {
		reason := "max_output_tokens"
		if finish == "content_filter" {
			reason = "content_filter"
		}
		result["incomplete_details"] = map[string]string{"reason": reason}
	}
	if err := emit(map[string]any{"type": "response." + status, "response": result}); err != nil {
		return nil, err
	}
	return result, nil
}
