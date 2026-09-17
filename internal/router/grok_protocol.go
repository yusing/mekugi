package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const grokModel = "grok:grok-4.6"

type grokTool struct{ name, namespace, kind string }
type grokTranslation struct {
	format   string
	openCode *openCodeService
	body     map[string]any
	tools    map[string]grokTool
	stream   bool
}

func grokToolName(namespace, name string) string {
	// The Grok proxy can omit bare "wait" calls while ending with tool_calls.
	// Keep Codex's identity, but use our existing
	// stable alias on the provider wire (including history and tool choice).
	if namespace == "" && name != "wait" && len(name) > 0 && len(name) <= 64 && !strings.HasPrefix(name, "_mekugi_") {
		valid := true
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				valid = false
				break
			}
		}
		if valid {
			return name
		}
	}
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return "_mekugi_" + hex.EncodeToString(sum[:16])
}

// Codex advertises Code Mode wait's integer fields as "number", but its native
// handler decodes them as unsigned integers. Grok's proxy serializes "number"
// values as floats (30000.0), which that handler rejects. Correct only this
// known host shape on the provider wire; leave argument bytes untouched.
func grokFunctionParameters(namespace, name string, parameters json.RawMessage) json.RawMessage {
	if namespace != "" || name != "wait" {
		return parameters
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(parameters, &schema) != nil {
		return parameters
	}
	var properties map[string]map[string]json.RawMessage
	if json.Unmarshal(schema["properties"], &properties) != nil ||
		jsonString(properties["cell_id"], "type") != "string" {
		return parameters
	}
	changed := false
	for _, field := range []string{"yield_time_ms", "max_tokens"} {
		if jsonString(properties[field], "type") == "number" {
			properties[field]["type"] = json.RawMessage(`"integer"`)
			changed = true
		}
	}
	if !changed {
		return parameters
	}
	schema["properties"] = mustMarshalJSON(properties)
	return mustMarshalJSON(schema)
}

// translateGrokRequest translates only representations with defined equivalents.
// In particular it never treats encrypted_content as text or silently removes an
// unsupported history item. The caller remains responsible for executing tools.
func translateGrokRequest(body []byte) (*grokTranslation, error) {
	return translateChatRequest(body, nil)
}

// translateChatRequest is the shared Responses-to-Chat adapter. OpenCode uses
// the same tool identity, validation and replay semantics as the xAI route.
func translateChatRequest(body []byte, service *openCodeService) (_ *grokTranslation, err error) {
	if service != nil {
		defer func() {
			if err == nil {
				return
			}
			if diagnostic, ok := errors.AsType[*requestCompatibilityError](err); ok {
				code := strings.Replace(diagnostic.code, "grok_", "opencode_", 1)
				err = incompatibleRequest(code, strings.ReplaceAll(diagnostic.message, "Grok", "OpenCode"))
			} else {
				err = incompatibleRequest("opencode_unsupported_request", strings.ReplaceAll(err.Error(), "Grok", "OpenCode"))
			}
		}()
	}
	request, err := parseResponsesRequest(body)
	if err != nil {
		return nil, err
	}
	model := "grok-4.6"
	if service == nil {
		if request.model() != grokModel {
			return nil, errors.New("unsupported Grok model; use grok:grok-4.6")
		}
	} else {
		var ok bool
		model, ok = strings.CutPrefix(request.model(), service.prefix+":")
		_, supported := service.model(model)
		if !ok || !supported {
			return nil, errors.New("unsupported OpenCode model")
		}
	}
	if value := jsonString(request.fields, "previous_response_id"); value != "" {
		return nil, errors.New("Grok requires explicit conversation history, not previous_response_id")
	}
	tr := &grokTranslation{openCode: service, body: map[string]any{"model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}, tools: make(map[string]grokTool), stream: request.streamResponse}
	if service != nil {
		if format := service.format(model); format != "chat" && format != "anthropic" && format != "responses" {
			return nil, errors.New("OpenCode model API format is missing or unsupported in the online catalog")
		}
		tr.format = service.format(model)
	}
	var tools []any
	var addTools func(json.RawMessage, string) error
	addTools = func(raw json.RawMessage, namespace string) error {
		var defs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &defs); err != nil {
			return fmt.Errorf("decode Grok tools: %w", err)
		}
		for _, def := range defs {
			kind, name := jsonString(def, "type"), jsonString(def, "name")
			if kind == "namespace" {
				if namespace != "" {
					return errors.New("nested tool namespaces are not supported by Grok")
				}
				if err := addTools(def["tools"], name); err != nil {
					return err
				}
				continue
			}
			switch kind {
			case "function", "custom":
				if name == "" {
					return errors.New("Grok tool has no name")
				}
				wireName := grokToolName(namespace, name)
				if _, exists := tr.tools[wireName]; exists {
					return errors.New("duplicate Grok tool identity")
				}
				tr.tools[wireName] = grokTool{name: name, namespace: namespace, kind: kind}
				fn := map[string]any{"name": wireName, "description": jsonString(def, "description")}
				if kind == "function" {
					parameters := def["parameters"]
					if len(parameters) == 0 {
						parameters = json.RawMessage(`{"type":"object","properties":{}}`)
					}
					fn["parameters"] = grokFunctionParameters(namespace, name, parameters)
					if strict, ok := def["strict"]; ok {
						fn["strict"] = strict
					}
				} else {
					fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string", "description": "The exact tool program or input, without JSON encoding or Markdown fences."}}, "required": []string{"input"}, "additionalProperties": false}
					if format, ok := def["format"]; ok {
						fn["description"] = jsonString(def, "description") + "\nThe input string must obey this tool format: " + string(format)
					}
				}
				tools = append(tools, map[string]any{"type": "function", "function": fn})
			case "web_search", "web_search_preview":
				// Provider-hosted search cannot run inside Codex. Do not advertise an
				// unavailable executor; explicitly describe this capability difference.
				tr.body["_no_hosted_search"] = true
			default:
				return fmt.Errorf("Grok does not support provider tool type %q", kind)
			}
		}
		return nil
	}
	if raw, ok := request.fields["tools"]; ok {
		if err := addTools(raw, ""); err != nil {
			return nil, err
		}
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &input); err != nil {
		return nil, errors.New("Grok requires an array of conversation items")
	}
	for _, item := range input {
		if jsonString(item, "type") == "additional_tools" {
			if err := addTools(item["tools"], ""); err != nil {
				return nil, err
			}
		}
	}
	messages := []map[string]any{}
	instructions := jsonString(request.fields, "instructions")
	if tr.body["_no_hosted_search"] == true {
		instructions += "\nOpenAI-hosted web search is unavailable on this Grok route. Use only the provided tools; do not claim to have performed unavailable searches."
		delete(tr.body, "_no_hosted_search")
	}
	if instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	for _, item := range input {
		kind := jsonString(item, "type")
		switch kind {
		case "additional_tools":
			continue
		case "message", "":
			role := jsonString(item, "role")
			if role == "developer" {
				role = "system"
			}
			switch role {
			case "system", "user", "assistant":
			default:
				return nil, fmt.Errorf("unsupported Grok message role %q", role)
			}
			content, err := grokContent(item["content"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		case "agent_message":
			content, err := grokContent(item["content"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "user", "content": content})
		case "function_call", "custom_tool_call":
			name := grokToolName(jsonString(item, "namespace"), jsonString(item, "name"))
			arguments := jsonString(item, "arguments")
			if kind == "custom_tool_call" {
				var input *string
				if json.Unmarshal(item["input"], &input) != nil || input == nil {
					return nil, errors.New("Grok custom tool history input is not a string")
				}
				arguments = string(mustMarshalJSON(map[string]string{"input": *input}))
			}
			if !json.Valid([]byte(arguments)) {
				return nil, errors.New("invalid JSON in Grok tool-call history")
			}
			call := map[string]any{"id": jsonString(item, "call_id"), "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}
			// Chat Completions requires parallel calls to share one assistant message.
			if len(messages) > 0 && messages[len(messages)-1]["role"] == "assistant" {
				last := messages[len(messages)-1]
				calls, _ := last["tool_calls"].([]any)
				last["tool_calls"] = append(calls, call)
			} else {
				messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{call}})
			}
		case "function_call_output", "custom_tool_call_output":
			content, err := grokContent(item["output"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": jsonString(item, "call_id"), "content": content})
		case "reasoning":
			if service != nil && tr.format != "chat" && jsonString(item, "encrypted_content") != "" {
				restored, err := restoreOpenCodeReasoning(service, model, jsonString(item, "encrypted_content"))
				if err != nil {
					return nil, err
				}
				if len(messages) == 0 || messages[len(messages)-1]["role"] != "assistant" {
					messages = append(messages, map[string]any{"role": "assistant", "content": nil})
				}
				last := messages[len(messages)-1]
				retained, _ := last["_opencode_reasoning"].([]any)
				last["_opencode_reasoning"] = append(retained, restored)
				continue
			}
			if encrypted := jsonString(item, "encrypted_content"); encrypted != "" {
				return nil, incompatibleRequest("grok_encrypted_history", "Grok cannot read encrypted OpenAI reasoning; start a fresh Grok thread or spawn with fork_turns=none.")
			}
			if service != nil {
				var summary []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(item["summary"], &summary); err != nil {
					return nil, errors.New("invalid OpenCode reasoning history")
				}
				var reasoning strings.Builder
				for _, part := range summary {
					if part.Type != "summary_text" {
						return nil, errors.New("unsupported OpenCode reasoning history")
					}
					reasoning.WriteString(part.Text)
				}
				if reasoning.Len() > 0 {
					if len(messages) == 0 || messages[len(messages)-1]["role"] != "assistant" {
						messages = append(messages, map[string]any{"role": "assistant", "content": nil})
					}
					messages[len(messages)-1]["reasoning_content"] = reasoning.String()
				}
			}
			// Unencrypted reasoning summaries are explanatory metadata, not messages.
		default:
			return nil, fmt.Errorf("Grok cannot translate history item type %q", kind)
		}
	}
	if service == nil || tr.format == "chat" {
		for _, message := range messages {
			parts, ok := message["content"].([]any)
			if !ok {
				continue
			}
			kept := make([]any, 0, len(parts))
			for _, part := range parts {
				fields, ok := part.(map[string]any)
				if ok && fields["type"] == "refusal" {
					if message["role"] != "assistant" {
						return nil, errors.New("refusal history must belong to an assistant")
					}
					message["refusal"] = fields["refusal"]
				} else {
					kept = append(kept, part)
				}
			}
			message["content"] = kept
			if len(kept) == 0 {
				message["content"] = nil
			}
		}
	}
	tr.body["messages"] = messages
	if len(tools) > 0 {
		tr.body["tools"] = tools
	}
	if raw, ok := request.fields["parallel_tool_calls"]; ok && len(tools) > 0 {
		tr.body["parallel_tool_calls"] = raw
	}
	if raw, ok := request.fields["tool_choice"]; ok && len(tools) > 0 {
		var choice string
		if json.Unmarshal(raw, &choice) == nil {
			tr.body["tool_choice"] = choice
		} else {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return nil, errors.New("unsupported Grok tool choice")
			}
			name := grokToolName(jsonString(obj, "namespace"), jsonString(obj, "name"))
			if _, ok := tr.tools[name]; !ok {
				return nil, errors.New("Grok tool choice references an unavailable tool")
			}
			tr.body["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": name}}
		}
	}
	if raw, ok := request.fields["reasoning"]; ok {
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(raw, &reasoning) != nil {
			return nil, errors.New("invalid Grok reasoning settings")
		}
		if service != nil {
			// Codex may inherit an effort from another model. Forward only
			// controls this model advertises; no manual clearing is needed.
			if reasoning.Effort != "" && slices.Contains(service.efforts(model), reasoning.Effort) {
				tr.body["reasoning_effort"] = reasoning.Effort
			}
		} else {
			switch reasoning.Effort {
			case "":
			case "low", "medium", "high", "xhigh":
				tr.body["reasoning_effort"] = reasoning.Effort
			default:
				return nil, errors.New("Grok supports reasoning effort low, medium, high or xhigh")
			}
		}
	}
	// Chat completion limits exclude reasoning, so they cannot enforce a
	// Responses total output budget. Reject it before sending an inference.
	if raw, ok := request.fields["max_output_tokens"]; ok && strings.TrimSpace(string(raw)) != "null" {
		if service == nil || tr.format == "chat" {
			return nil, errors.New("Grok Chat Completions cannot enforce max_output_tokens including reasoning; omit this unsupported setting")
		}
		tr.body["max_output_tokens"] = raw
	}
	for _, field := range []string{"temperature", "top_p"} {
		if raw, ok := request.fields[field]; ok {
			tr.body[field] = raw
		}
	}
	if raw, ok := request.fields["text"]; ok {
		var text map[string]json.RawMessage
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		if format, ok := text["format"]; ok {
			var f map[string]json.RawMessage
			if err := json.Unmarshal(format, &f); err != nil {
				return nil, err
			}
			switch jsonString(f, "type") {
			case "text", "":
			case "json_object":
				tr.body["response_format"] = map[string]string{"type": "json_object"}
			case "json_schema":
				delete(f, "type")
				tr.body["response_format"] = map[string]any{"type": "json_schema", "json_schema": f}
			default:
				return nil, errors.New("unsupported Grok structured output format")
			}
		}
	}
	if service != nil && tr.format != "chat" {
		if err := tr.convertOpenCodeRequest(); err != nil {
			return nil, err
		}
	}
	return tr, nil
}

func grokContent(raw json.RawMessage) (any, error) {
	var text *string
	if json.Unmarshal(raw, &text) == nil && text != nil {
		return *text, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || parts == nil {
		return nil, errors.New("unsupported Grok message content")
	}
	if len(parts) == 0 {
		return "", nil
	}
	var content []any
	for _, part := range parts {
		switch jsonString(part, "type") {
		case "input_text", "output_text", "text":
			value := jsonString(part, "text")
			content = append(content, map[string]string{"type": "text", "text": value})
		case "input_image":
			imageURL := jsonString(part, "image_url")
			if imageURL == "" {
				return nil, errors.New("Grok requires an image URL or data URL, not a provider file ID")
			}
			image := map[string]string{"url": imageURL}
			if detail := jsonString(part, "detail"); detail != "" {
				image["detail"] = detail
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": image})
		case "refusal":
			content = append(content, map[string]any{"type": "refusal", "refusal": jsonString(part, "refusal")})
		case "encrypted_content":
			return nil, incompatibleRequest("grok_encrypted_history", "Grok cannot read encrypted agent messages; start a fresh thread with the Grok collaboration bridge enabled.")
		default:
			return nil, fmt.Errorf("unsupported Grok content type %q", jsonString(part, "type"))
		}
	}
	return content, nil
}
