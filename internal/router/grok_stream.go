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

	"github.com/yusing/mekugi/internal/chat"
	responseevents "github.com/yusing/mekugi/internal/responses"
)

type grokChunk struct {
	RetainedReasoning json.RawMessage `json:"_mekugi_reasoning"`
	Model             string          `json:"model"`
	Choices           []struct {
		Index int `json:"index"`
		Delta struct {
			Refusal          string `json:"refusal"`
			ReasoningContent string `json:"reasoning_content"`
			Content          string `json:"content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason chat.FinishReason `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		ReasoningTokens  int64 `json:"reasoning_tokens"`
		PromptDetails    struct {
			CacheWriteTokens int64 `json:"cache_write_tokens"`
			CachedTokens     int64 `json:"cached_tokens"`
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
func (tr *grokTranslation) readGrokStream(reader io.Reader, emit func(map[string]any) error) (result map[string]any, streamErr error) {
	if tr.openCode != nil {
		defer func() {
			if diagnostic, ok := errors.AsType[*criticalDiagnosticError](streamErr); ok {
				streamErr = staticCriticalDiagnostic(strings.Replace(diagnostic.code, "grok_", "opencode_", 1), strings.ReplaceAll(diagnostic.summary, "Grok", "OpenCode"))
			}
		}()
	}
	writeFailed := false
	write := emit
	emit = func(event map[string]any) error {
		err := write(event)
		writeFailed = writeFailed || err != nil
		return err
	}
	id := "resp_" + rand.Text()
	messageID := "msg_" + rand.Text()
	model := "grok-4.6"
	if tr.openCode != nil {
		model, _ = tr.body["model"].(string)
	}
	var retainedReasoning []any
	reasoning := strings.Builder{}
	refusal := false
	textPart := func(value string) map[string]any {
		if refusal {
			return map[string]any{"type": "refusal", "refusal": value}
		}
		return map[string]any{"type": "output_text", "text": value, "annotations": []any{}}
	}
	textEvent := func(suffix string) string {
		if refusal {
			return "response.refusal." + suffix
		}
		return "response.output_text." + suffix
	}
	text := strings.Builder{}
	calls := map[int]*grokStreamCall{}
	var finish chat.FinishReason
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
	if err := emit(map[string]any{"type": responseevents.Created, "response": response("in_progress")}); err != nil {
		return nil, err
	}
	// Report producer failures as protocol failures, not an unexplained EOF.
	// Only producer-owned summaries may cross this boundary; emit/write errors
	// remain transport errors and must not trigger another write.
	defer func() {
		diagnostic, ok := errors.AsType[*criticalDiagnosticError](streamErr)
		if !ok || writeFailed {
			return
		}
		failed := response("failed")
		code, summary := diagnostic.code, diagnostic.summary
		if tr.openCode != nil {
			code = strings.Replace(code, "grok_", "opencode_", 1)
			summary = strings.ReplaceAll(summary, "Grok", "OpenCode")
		}
		failed["error"] = map[string]string{"code": code, "message": summary}
		if err := emit(map[string]any{"type": responseevents.Failed, "response": failed}); err != nil {
			streamErr = errors.Join(streamErr, err)
		}
	}()
	consume := func(data string) error {
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk grokChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return staticCriticalDiagnostic("grok_stream_invalid_json", "invalid JSON in Grok response stream")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return staticCriticalDiagnostic("grok_stream_provider_error", "Grok reported a streaming error")
		}
		if tr.openCode != nil && tr.format != "chat" && len(chunk.RetainedReasoning) > 0 {
			var item map[string]any
			if json.Unmarshal(chunk.RetainedReasoning, &item) != nil {
				return openCodeStreamError("Invalid retained OpenCode reasoning")
			}
			model, _ := tr.body["model"].(string)
			retainedReasoning = append(retainedReasoning, map[string]any{
				"type": "reasoning", "id": "rs_" + rand.Text(), "summary": []any{},
				"encrypted_content": sealOpenCodeReasoning(tr.openCode, model, chunk.RetainedReasoning),
			})
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
			outputTokens := u.CompletionTokens
			if tr.openCode == nil {
				outputTokens += u.CompletionDetails.ReasoningTokens
			}
			usage = map[string]any{"input_tokens": u.PromptTokens, "output_tokens": outputTokens, "total_tokens": u.PromptTokens + outputTokens, "input_tokens_details": map[string]any{"cached_tokens": u.PromptDetails.CachedTokens, "cache_write_tokens": u.PromptDetails.CacheWriteTokens}, "output_tokens_details": map[string]any{"reasoning_tokens": u.CompletionDetails.ReasoningTokens}}
		}
		textEmitted := false
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return staticCriticalDiagnostic("grok_stream_multiple_choices", "Grok returned multiple completion choices")
			}
			// A terminal choice seals its data, including incomplete calls.
			// Usage-only trailers have no choices and remain admissible.
			if finish != "" {
				return staticCriticalDiagnostic("grok_stream_data_after_terminal", "Grok returned choice data after its terminal finish reason")
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			if tr.openCode != nil {
				reasoning.WriteString(choice.Delta.ReasoningContent)
			}
			value := choice.Delta.Content
			if choice.Delta.Refusal != "" {
				if value != "" || (messageStarted && !refusal) {
					return openCodeStreamError("Mixed text and refusal output is unsupported")
				}
				refusal = true
				value = choice.Delta.Refusal
			} else if value != "" && refusal {
				return openCodeStreamError("Text followed a refusal output")
			}
			if value != "" {
				if !messageStarted {
					messageStarted = true
					item := map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "in_progress", "phase": "final_answer", "content": []any{}}
					if err := emit(map[string]any{"type": responseevents.OutputItemAdded, "output_index": 0, "item": item}); err != nil {
						return err
					}
					if err := emit(map[string]any{"type": responseevents.ContentPartAdded, "item_id": messageID, "output_index": 0, "content_index": 0, "part": textPart("")}); err != nil {
						return err
					}
				}
				text.WriteString(value)
				textEmitted = true
				if err := emit(map[string]any{"type": textEvent("delta"), "item_id": messageID, "output_index": 0, "content_index": 0, "delta": value}); err != nil {
					return err
				}
			}
			for _, part := range choice.Delta.ToolCalls {
				if part.Index < 0 || part.Index > 1023 {
					return staticCriticalDiagnostic("grok_stream_call_budget", "Grok tool-call index exceeds the response budget")
				}
				call := calls[part.Index]
				if call == nil {
					call = &grokStreamCall{}
					calls[part.Index] = call
				}
				if part.ID != "" {
					if call.id != "" && call.id != part.ID {
						return staticCriticalDiagnostic("grok_stream_call_identity_changed", "Grok changed a tool-call identity")
					}
					call.id = part.ID
				}
				call.name.WriteString(part.Function.Name)
				call.arguments.WriteString(part.Function.Arguments)
			}
		}
		if !textEmitted {
			return emit(map[string]any{"type": responseevents.InProgress, "response": map[string]any{"id": id, "status": "in_progress"}})
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
			return nil, staticCriticalDiagnostic("grok_stream_buffer_budget", "Grok response exceeds the router buffer budget")
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
		return nil, fmt.Errorf("read Grok stream: %w", forwardCriticalDiagnostic(err))
	}
	if !done && len(data) > 0 {
		if err := consume(strings.Join(data, "\n")); err != nil {
			return nil, err
		}
	}
	if !done {
		return nil, staticCriticalDiagnostic("grok_stream_missing_done", "Grok stream ended without [DONE]")
	}
	status := finish.ResponseStatus()
	if status == "" {
		return nil, staticCriticalDiagnostic("grok_stream_finish_reason", "Grok stream has no supported terminal finish reason")
	}
	if finish == chat.ToolCalls && len(calls) == 0 {
		return nil, staticCriticalDiagnostic("grok_stream_missing_calls", "Grok finished tool calls without a call")
	}
	if len(calls) > 0 && finish != chat.ToolCalls {
		return nil, staticCriticalDiagnostic("grok_stream_incomplete_calls", "Grok tool arguments were not completed")
	}
	if messageStarted {
		part := textPart(text.String())
		item := map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{part}}
		if len(calls) > 0 {
			item["phase"] = "commentary"
		}
		output = append(output, item)
		doneField := "text"
		if refusal {
			doneField = "refusal"
		}
		for _, event := range []map[string]any{
			{"type": textEvent("done"), "item_id": messageID, "output_index": 0, "content_index": 0, doneField: text.String()},
			{"type": responseevents.ContentPartDone, "item_id": messageID, "output_index": 0, "content_index": 0, "part": part},
			{"type": responseevents.OutputItemDone, "output_index": 0, "item": item},
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
			return nil, staticCriticalDiagnostic("grok_stream_call_indexes", "Grok returned noncontiguous tool-call indexes")
		}
		tool, ok := tr.tools[call.name.String()]
		if !ok {
			return nil, staticCriticalDiagnostic("grok_stream_unavailable_tool", "Grok returned an unavailable tool")
		}
		if call.id == "" || seen[call.id] {
			return nil, staticCriticalDiagnostic("grok_stream_call_identity", "Grok returned an invalid tool-call identity")
		}
		seen[call.id] = true
		arguments := call.arguments.String()
		if !json.Valid([]byte(arguments)) {
			return nil, staticCriticalDiagnostic("grok_stream_call_arguments", "Grok returned invalid tool-call arguments")
		}
		item := map[string]any{"type": "function_call", "id": "fc_" + rand.Text(), "call_id": call.id, "name": tool.name, "arguments": arguments, "status": "completed"}
		if tool.namespace != "" {
			item["namespace"] = tool.namespace
		}
		if tool.kind == "custom" {
			var args map[string]json.RawMessage
			if json.Unmarshal([]byte(arguments), &args) != nil || len(args) != 1 {
				return nil, staticCriticalDiagnostic("grok_stream_custom_input_shape", "Grok custom tool requires exactly one input string")
			}
			var input *string
			if json.Unmarshal(args["input"], &input) != nil || input == nil {
				return nil, staticCriticalDiagnostic("grok_stream_custom_input_type", "Grok custom tool input is not a string")
			}
			delete(item, "arguments")
			item["type"] = "custom_tool_call"
			item["input"] = *input
		}
		callItems = append(callItems, item)
	}
	if reasoning.Len() > 0 {
		index := len(output)
		item := map[string]any{"type": "reasoning", "id": "rs_" + rand.Text(), "summary": []any{
			map[string]string{"type": "summary_text", "text": reasoning.String()},
		}}
		output = append(output, item)
		for _, kind := range []string{responseevents.OutputItemAdded, responseevents.OutputItemDone} {
			if err := emit(map[string]any{"type": kind, "output_index": index, "item": item}); err != nil {
				return nil, err
			}
		}
	}
	for _, item := range retainedReasoning {
		index := len(output)
		output = append(output, item)
		for _, kind := range []string{responseevents.OutputItemAdded, responseevents.OutputItemDone} {
			if err := emit(map[string]any{"type": kind, "output_index": index, "item": item}); err != nil {
				return nil, err
			}
		}
	}
	for _, item := range callItems {
		index := len(output)
		output = append(output, item)
		// Emit the complete Responses tool lifecycle only after every call has
		// validated. Router transforms use input/arguments.done as the handoff
		// boundary, even though Chat Completions buffers the whole call.
		field, doneType := "arguments", responseevents.FunctionArgumentsDone
		if item["type"] == responseevents.CustomToolCall {
			field, doneType = "input", responseevents.CustomInputDone
		}
		added := maps.Clone(item)
		added["status"] = "in_progress"
		added[field] = ""
		if err := emit(map[string]any{"type": responseevents.OutputItemAdded, "output_index": index, "item": added}); err != nil {
			return nil, err
		}
		doneEvent := map[string]any{"type": doneType, "output_index": index, "item_id": item["id"], "call_id": item["call_id"], field: item[field]}
		if item["type"] == responseevents.FunctionCall {
			doneEvent["name"] = item["name"]
		}
		if err := emit(doneEvent); err != nil {
			return nil, err
		}
		if err := emit(map[string]any{"type": responseevents.OutputItemDone, "output_index": index, "item": item}); err != nil {
			return nil, err
		}
	}
	result = response(status)
	if reason := finish.IncompleteReason(); reason != "" {
		result["incomplete_details"] = map[string]string{"reason": reason}
	}
	if err := emit(map[string]any{"type": "response." + status, "response": result}); err != nil {
		return nil, err
	}
	return result, nil
}
