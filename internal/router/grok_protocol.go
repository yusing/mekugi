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
	providerFailureDetail func([]byte) string
	format                string
	openCode              *openCodeService
	body                  map[string]any
	tools                 map[string]grokTool
	stream                bool
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

// translateChatRequest validates shared tool identity and history, then builds
// the selected provider's endpoint request without an intermediate Chat request.
// It translates only representations with defined equivalents, never treating
// encrypted_content as text or silently removing unsupported history items.
// The caller remains responsible for executing tools.
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
	tr := &grokTranslation{openCode: service, body: map[string]any{"model": model, "stream": true}, tools: make(map[string]grokTool), stream: request.streamResponse}
	if service != nil {
		if format := service.format(model); format != "chat" && format != "anthropic" && format != "responses" {
			return nil, errors.New("OpenCode model API format is missing or unsupported in the online catalog")
		}
		tr.format = service.format(model)
	}
	outputConfig := map[string]any{}
	switch tr.format {
	case "anthropic":
		// Messages requires an output ceiling, even when the caller omits one.
		tr.body["max_tokens"] = 32768
	case "responses":
		tr.body["store"] = false
		tr.body["include"] = []string{"reasoning.encrypted_content"}
	default:
		tr.body["stream_options"] = map[string]any{"include_usage": true}
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
				switch tr.format {
				case "anthropic":
					tools = append(tools, map[string]any{"name": fn["name"], "description": fn["description"], "input_schema": fn["parameters"]})
				case "responses":
					fn["type"] = "function"
					tools = append(tools, fn)
				default:
					tools = append(tools, map[string]any{"type": "function", "function": fn})
				}
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
	messages := []providerMessage{}
	instructions := jsonString(request.fields, "instructions")
	if tr.body["_no_hosted_search"] == true {
		instructions += "\nOpenAI-hosted web search is unavailable on this Grok route. Use only the provided tools; do not claim to have performed unavailable searches."
		delete(tr.body, "_no_hosted_search")
	}
	if instructions != "" {
		messages = append(messages, providerMessage{role: "system", content: instructions})
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
			messages = append(messages, providerMessage{role: role, content: content})
		case "agent_message":
			content, err := grokContent(item["content"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, providerMessage{role: "user", content: content})
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
			call := providerCall{id: jsonString(item, "call_id"), name: name, arguments: arguments}
			if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
				messages = append(messages, providerMessage{role: "assistant"})
			}
			last := &messages[len(messages)-1]
			last.calls = append(last.calls, call)
		case "function_call_output", "custom_tool_call_output":
			content, err := grokContent(item["output"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, providerMessage{role: "tool", callID: jsonString(item, "call_id"), content: content})
		case "reasoning":
			if service != nil && tr.format != "chat" && jsonString(item, "encrypted_content") != "" {
				restored, err := restoreOpenCodeReasoning(service, model, jsonString(item, "encrypted_content"))
				if err != nil {
					return nil, err
				}
				if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
					messages = append(messages, providerMessage{role: "assistant"})
				}
				last := &messages[len(messages)-1]
				last.retained = append(last.retained, restored)
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
					if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
						messages = append(messages, providerMessage{role: "assistant"})
					}
					messages[len(messages)-1].reasoning = reasoning.String()
				}
			}
			// Unencrypted reasoning summaries are explanatory metadata, not messages.
		default:
			return nil, fmt.Errorf("Grok cannot translate history item type %q", kind)
		}
	}

	if len(tools) > 0 {
		tr.body["tools"] = tools
	}
	if raw, ok := request.fields["tool_choice"]; ok && len(tools) > 0 {
		var choice string
		if json.Unmarshal(raw, &choice) == nil {
			if tr.format == "anthropic" {
				if choice == "required" {
					choice = "any"
				}
				tr.body["tool_choice"] = map[string]any{"type": choice}
			} else {
				tr.body["tool_choice"] = choice
			}
		} else {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return nil, errors.New("unsupported Grok tool choice")
			}
			name := grokToolName(jsonString(obj, "namespace"), jsonString(obj, "name"))
			if _, ok := tr.tools[name]; !ok {
				return nil, errors.New("Grok tool choice references an unavailable tool")
			}
			switch tr.format {
			case "anthropic":
				tr.body["tool_choice"] = map[string]any{"type": "tool", "name": name}
			case "responses":
				tr.body["tool_choice"] = map[string]any{"type": "function", "name": name}
			default:
				tr.body["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": name}}
			}
		}
	}
	if raw, ok := request.fields["parallel_tool_calls"]; ok && len(tools) > 0 {
		if tr.format == "anthropic" {
			var enabled bool
			if json.Unmarshal(raw, &enabled) != nil {
				return nil, errors.New("invalid OpenCode parallel tool setting")
			}
			choice, _ := tr.body["tool_choice"].(map[string]any)
			if choice == nil {
				choice = map[string]any{"type": "auto"}
			}
			choice["disable_parallel_tool_use"] = !enabled
			tr.body["tool_choice"] = choice
		} else {
			tr.body["parallel_tool_calls"] = raw
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
				switch tr.format {
				case "anthropic":
					outputConfig["effort"] = reasoning.Effort
				case "responses":
					tr.body["reasoning"] = map[string]string{"effort": reasoning.Effort}
				default:
					tr.body["reasoning_effort"] = reasoning.Effort
				}
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
		if tr.format == "anthropic" {
			tr.body["max_tokens"] = raw
		} else {
			tr.body["max_output_tokens"] = raw
		}
	}
	for _, field := range []string{"temperature", "top_p", "service_tier"} {
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
				switch tr.format {
				case "anthropic":
					return nil, errors.New("OpenCode Messages requires a JSON schema for structured output")
				case "responses":
					tr.body["text"] = map[string]any{"format": map[string]string{"type": "json_object"}}
				default:
					tr.body["response_format"] = map[string]string{"type": "json_object"}
				}
			case "json_schema":
				switch tr.format {
				case "anthropic":
					outputConfig["format"] = map[string]any{"type": "json_schema", "schema": f["schema"]}
				case "responses":
					tr.body["text"] = map[string]any{"format": f}
				default:
					delete(f, "type")
					tr.body["response_format"] = map[string]any{"type": "json_schema", "json_schema": f}
				}
			default:
				return nil, errors.New("unsupported Grok structured output format")
			}
		}
	}
	if len(outputConfig) > 0 {
		tr.body["output_config"] = outputConfig
	}
	if err := tr.writeProviderMessages(messages); err != nil {
		return nil, err
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
