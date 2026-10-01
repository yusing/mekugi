package router

import (
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

type providerChunk struct {
	RetainedReasoning json.RawMessage  `json:"_mekugi_reasoning"`
	Model             string           `json:"model"`
	Choices           []providerChoice `json:"choices"`
	Usage             *providerUsage   `json:"usage"`
	Error             json.RawMessage  `json:"error"`
}
type providerChoice struct {
	Index        int               `json:"index"`
	Delta        providerDelta     `json:"delta"`
	FinishReason chat.FinishReason `json:"finish_reason"`
}
type providerDelta struct {
	Refusal          string              `json:"refusal"`
	ReasoningContent string              `json:"reasoning_content"`
	Content          string              `json:"content"`
	ToolCalls        []providerCallDelta `json:"tool_calls"`
}
type providerCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type providerUsage struct {
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
}
type streamedReasoning struct {
	id    string
	text  strings.Builder
	index int
	open  bool // Emitted as the active item; closes before any other item starts.
}
type providerStreamCall struct {
	id              string
	name, arguments strings.Builder
}

// readProviderStream uses the selected endpoint only for normalization. Validation
// and Responses lifecycle emission have one owner below.
func (tr *providerTranslation) readProviderStream(reader io.Reader, emit func(map[string]any) error) (_ map[string]any, err error) {
	defer func() {
		if err != nil && tr.policy.closeFailedStream {
			if closer, ok := reader.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}()
	return tr.consumeProviderStream(func(send func(providerChunk) error) error {
		return tr.endpoint.readStream(reader, tr.policy.diagnostics, send)
	}, emit)
}

// consumeProviderStream owns validation and Responses emission for every
// provider. Sources return nil only after their protocol's terminal marker.
// Source: internal/router/grok_stream.go:78:452@413bbaa9b9202042199c753690febe7bc1358fa6 consumeProviderStream
func (tr *providerTranslation) consumeProviderStream(source func(func(providerChunk) error) error, emit func(map[string]any) error) (result map[string]any, streamErr error) {
	d := tr.policy.diagnostics
	writeFailed := false
	write := emit
	emit = func(event map[string]any) error {
		err := write(event)
		writeFailed = writeFailed || err != nil
		return err
	}
	id := "resp_" + rand.Text()
	messageID := "msg_" + rand.Text()
	model, _ := tr.body["model"].(string)
	// Visible provider reasoning streams as one summary item per provider
	// reasoning block. Codex tracks one active item, so the block closes
	// before text starts; reasoning seen after text is held for the end.
	// Replay-bound routes attach it to the retained provider item, since
	// their history cannot replay summary-only reasoning.
	output := []any{}
	var thinking *streamedReasoning
	var retainedReasoning []any
	summaryOnly := tr.policy.sealReasoning == nil
	reasoningItem := func(r *streamedReasoning, retained json.RawMessage) map[string]any {
		item := map[string]any{"type": "reasoning", "id": "rs_" + rand.Text(), "summary": []any{}}
		if r != nil {
			item["id"] = r.id
			if r.text.Len() > 0 && (summaryOnly || retained != nil) {
				item["summary"] = []any{map[string]string{"type": "summary_text", "text": r.text.String()}}
			}
		}
		if retained != nil {
			item["encrypted_content"] = tr.policy.sealReasoning(retained)
		}
		return item
	}
	closeReasoning := func(item map[string]any) error {
		r := thinking
		thinking = nil
		if item == nil {
			if r == nil {
				return nil
			}
			item = reasoningItem(r, nil)
		}
		if r == nil || !r.open {
			if len(item["summary"].([]any)) > 0 || item["encrypted_content"] != nil {
				retainedReasoning = append(retainedReasoning, item)
			}
			return nil
		}
		part := map[string]string{"type": "summary_text", "text": r.text.String()}
		output = append(output, item)
		for _, event := range []map[string]any{
			{"type": responseevents.ReasoningTextDone, "item_id": r.id, "output_index": r.index, "summary_index": 0, "text": part["text"]},
			{"type": responseevents.ReasoningPartDone, "item_id": r.id, "output_index": r.index, "summary_index": 0, "part": part},
			{"type": responseevents.OutputItemDone, "output_index": r.index, "item": item},
		} {
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	}
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
	calls := map[int]*providerStreamCall{}
	var finish chat.FinishReason
	var usage any
	messageStarted := false
	messageIndex := 0
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
	// Provider details are sanitized at the authenticated boundary; protocol
	// failures retain static summaries. Write errors must not trigger another write.
	defer func() {
		diagnostic, ok := errors.AsType[*criticalDiagnosticError](streamErr)
		if !ok || writeFailed {
			return
		}
		failed := response("failed")
		code, summary := diagnostic.code, diagnostic.summary
		if diagnostic.callerDetail != "" {
			summary = diagnostic.callerDetail
		}
		failed["error"] = map[string]string{"code": code, "message": summary}
		if err := emit(map[string]any{"type": responseevents.Failed, "response": failed}); err != nil {
			streamErr = errors.Join(streamErr, err)
		}
	}()
	consume := func(chunk providerChunk) error {
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			var detail string
			if tr.providerFailureDetail != nil {
				detail = tr.providerFailureDetail(chunk.Error)
			} else {
				detail = newProviderHTTPError(d.name, 0, chunk.Error).Error()
			}
			return &criticalDiagnosticError{
				err:  errors.New("provider reported a streaming error"),
				code: d.prefix + "_stream_provider_error", summary: d.name + " reported a streaming error",
				callerDetail: detail, distinct: true,
			}
		}
		if tr.policy.sealReasoning != nil && len(chunk.RetainedReasoning) > 0 {
			var item map[string]any
			if json.Unmarshal(chunk.RetainedReasoning, &item) != nil {
				return d.streamError("invalid", "Invalid retained "+d.name+" reasoning")
			}
			if err := closeReasoning(reasoningItem(thinking, chunk.RetainedReasoning)); err != nil {
				return err
			}
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
			if !tr.policy.completionIncludesReasoning {
				outputTokens += u.CompletionDetails.ReasoningTokens
			}
			usage = map[string]any{"input_tokens": u.PromptTokens, "output_tokens": outputTokens, "total_tokens": u.PromptTokens + outputTokens, "input_tokens_details": map[string]any{"cached_tokens": u.PromptDetails.CachedTokens, "cache_write_tokens": u.PromptDetails.CacheWriteTokens}, "output_tokens_details": map[string]any{"reasoning_tokens": u.CompletionDetails.ReasoningTokens}}
		}
		textEmitted := false
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return d.streamError("multiple_choices", fmt.Sprintf("%s returned multiple completion choices", d.name))
			}
			// A terminal choice seals its data, including incomplete calls.
			// Usage-only trailers have no choices and remain admissible.
			if finish != "" {
				return d.streamError("data_after_terminal", fmt.Sprintf("%s returned choice data after its terminal finish reason", d.name))
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			if delta := choice.Delta.ReasoningContent; delta != "" {
				if thinking == nil {
					thinking = &streamedReasoning{id: "rs_" + rand.Text()}
				}
				thinking.text.WriteString(delta)
				if !messageStarted {
					if !thinking.open {
						thinking.open, thinking.index = true, len(output)
						for _, event := range []map[string]any{
							{"type": responseevents.OutputItemAdded, "output_index": thinking.index, "item": map[string]any{"type": "reasoning", "id": thinking.id, "summary": []any{}}},
							{"type": responseevents.ReasoningPartAdded, "item_id": thinking.id, "output_index": thinking.index, "summary_index": 0, "part": map[string]string{"type": "summary_text", "text": ""}},
						} {
							if err := emit(event); err != nil {
								return err
							}
						}
					}
					if err := emit(map[string]any{"type": responseevents.ReasoningTextDelta, "item_id": thinking.id, "output_index": thinking.index, "summary_index": 0, "delta": delta}); err != nil {
						return err
					}
					textEmitted = true
				}
			}
			value := choice.Delta.Content
			if choice.Delta.Refusal != "" {
				if value != "" || (messageStarted && !refusal) {
					return d.contentError("Mixed text and refusal output is unsupported")
				}
				refusal = true
				value = choice.Delta.Refusal
			} else if value != "" && refusal {
				return d.contentError("Text followed a refusal output")
			}
			if value != "" {
				if !messageStarted {
					if thinking != nil && thinking.open {
						if err := closeReasoning(nil); err != nil {
							return err
						}
					}
					messageIndex = len(output)
					messageStarted = true
					item := map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "in_progress", "phase": "final_answer", "content": []any{}}
					if err := emit(map[string]any{"type": responseevents.OutputItemAdded, "output_index": messageIndex, "item": item}); err != nil {
						return err
					}
					if err := emit(map[string]any{"type": responseevents.ContentPartAdded, "item_id": messageID, "output_index": messageIndex, "content_index": 0, "part": textPart("")}); err != nil {
						return err
					}
				}
				text.WriteString(value)
				textEmitted = true
				if err := emit(map[string]any{"type": textEvent("delta"), "item_id": messageID, "output_index": messageIndex, "content_index": 0, "delta": value}); err != nil {
					return err
				}
			}
			for _, part := range choice.Delta.ToolCalls {
				if part.Index < 0 || part.Index > 1023 {
					return d.streamError("call_budget", fmt.Sprintf("%s tool-call index exceeds the response budget", d.name))
				}
				call := calls[part.Index]
				if call == nil {
					call = &providerStreamCall{}
					calls[part.Index] = call
				}
				if part.ID != "" {
					if call.id != "" && call.id != part.ID {
						return d.streamError("call_identity_changed", fmt.Sprintf("%s changed a tool-call identity", d.name))
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
	if err := source(consume); err != nil {
		return nil, err
	}
	status := finish.ResponseStatus()
	if status == "" {
		return nil, d.streamError("finish_reason", fmt.Sprintf("%s stream has no supported terminal finish reason", d.name))
	}
	if finish == chat.ToolCalls && len(calls) == 0 {
		return nil, d.streamError("missing_calls", fmt.Sprintf("%s finished tool calls without a call", d.name))
	}
	if len(calls) > 0 && finish != chat.ToolCalls {
		return nil, d.streamError("incomplete_calls", fmt.Sprintf("%s tool arguments were not completed", d.name))
	}
	if err := closeReasoning(nil); err != nil {
		return nil, err
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
			{"type": textEvent("done"), "item_id": messageID, "output_index": messageIndex, "content_index": 0, doneField: text.String()},
			{"type": responseevents.ContentPartDone, "item_id": messageID, "output_index": messageIndex, "content_index": 0, "part": part},
			{"type": responseevents.OutputItemDone, "output_index": messageIndex, "item": item},
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
			return nil, d.streamError("call_indexes", fmt.Sprintf("%s returned noncontiguous tool-call indexes", d.name))
		}
		tool, ok := tr.tools[call.name.String()]
		if !ok {
			return nil, d.streamError("unavailable_tool", fmt.Sprintf("%s returned an unavailable tool", d.name))
		}
		if call.id == "" || seen[call.id] {
			return nil, d.streamError("call_identity", fmt.Sprintf("%s returned an invalid tool-call identity", d.name))
		}
		seen[call.id] = true
		arguments := call.arguments.String()
		if !json.Valid([]byte(arguments)) {
			return nil, d.streamError("call_arguments", fmt.Sprintf("%s returned invalid tool-call arguments", d.name))
		}
		item := map[string]any{"type": "function_call", "id": "fc_" + rand.Text(), "call_id": call.id, "name": tool.name, "arguments": arguments, "status": "completed"}
		if tool.namespace != "" {
			item["namespace"] = tool.namespace
		}
		if tool.kind == "custom" {
			var args map[string]json.RawMessage
			if json.Unmarshal([]byte(arguments), &args) != nil || len(args) != 1 {
				return nil, d.streamError("custom_input_shape", fmt.Sprintf("%s custom tool requires exactly one input string", d.name))
			}
			var input *string
			if json.Unmarshal(args["input"], &input) != nil || input == nil {
				return nil, d.streamError("custom_input_type", fmt.Sprintf("%s custom tool input is not a string", d.name))
			}
			delete(item, "arguments")
			item["type"] = "custom_tool_call"
			item["input"] = *input
		}
		callItems = append(callItems, item)
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
