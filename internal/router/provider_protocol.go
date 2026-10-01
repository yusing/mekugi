package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const grokModel = "grok:grok-4.6"

type providerTool struct{ name, namespace, kind string }
type providerTranslation struct {
	policy                translationPolicy
	endpoint              providerEndpoint
	providerFailureDetail func([]byte) string
	body                  map[string]any
	tools                 map[string]providerTool
	stream                bool
}

func providerToolName(namespace, name string) string {
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
func providerFunctionParameters(namespace, name string, parameters json.RawMessage) json.RawMessage {
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

// translateResponsesForProvider validates shared tool identity and history, then builds
// the selected provider's endpoint request without an intermediate Chat request.
// It translates only representations with defined equivalents, never treating
// encrypted_content as text or silently removing unsupported history items.
// The caller remains responsible for executing tools.
// Source: internal/router/grok_protocol.go:81:477@413bbaa9b9202042199c753690febe7bc1358fa6 translateChatRequest
func translateResponsesForProvider(request parsedResponsesRequest, model string, policy translationPolicy, endpoint providerEndpoint) (*providerTranslation, error) {
	d := policy.diagnostics
	if value := jsonString(request.fields, "previous_response_id"); value != "" {
		return nil, fmt.Errorf("%s requires explicit conversation history, not previous_response_id", d.subject)
	}
	tr := &providerTranslation{policy: policy, endpoint: endpoint, tools: make(map[string]providerTool), stream: request.streamResponse}
	noHostedSearch := false
	var tools []map[string]any
	var addTools func(json.RawMessage, string) error
	addTools = func(raw json.RawMessage, namespace string) error {
		var defs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &defs); err != nil {
			return fmt.Errorf("decode %s tools: %w", d.name, err)
		}
		for _, def := range defs {
			kind, name := jsonString(def, "type"), jsonString(def, "name")
			if kind == "namespace" {
				if namespace != "" {
					return fmt.Errorf("nested tool namespaces are not supported by %s", d.name)
				}
				if err := addTools(def["tools"], name); err != nil {
					return err
				}
				continue
			}
			switch kind {
			case "function", "custom":
				if name == "" {
					return fmt.Errorf("%s tool has no name", d.subject)
				}
				wireName := providerToolName(namespace, name)
				if _, exists := tr.tools[wireName]; exists {
					return fmt.Errorf("duplicate %s tool identity", d.name)
				}
				tr.tools[wireName] = providerTool{name: name, namespace: namespace, kind: kind}
				fn := map[string]any{"name": wireName, "description": jsonString(def, "description")}
				if kind == "function" {
					parameters := def["parameters"]
					if len(parameters) == 0 {
						parameters = json.RawMessage(`{"type":"object","properties":{}}`)
					}
					fn["parameters"] = providerFunctionParameters(namespace, name, parameters)
					if strict, ok := def["strict"]; ok {
						fn["strict"] = strict
					}
				} else {
					fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string", "description": embeddedInstruction("grok_input")}}, "required": []string{"input"}, "additionalProperties": false}
					if namespace == "" && name == "exec" {
						// Codex's description leaves implicit that unprinted nested
						// results never reach the model; other models miss it.
						fn["description"] = fn["description"].(string) + "\n" + embeddedInstruction("grok_exec")
					}
					if format, ok := def["format"]; ok {
						fn["description"] = fn["description"].(string) + "\n" + embeddedInstruction("grok_format_prefix") + " " + string(format)
					}
				}
				tools = append(tools, fn)
			case "web_search", "web_search_preview":
				// Provider-hosted search cannot run inside Codex. Do not advertise an
				// unavailable executor; explicitly describe this capability difference.
				noHostedSearch = true
			default:
				return fmt.Errorf("%s does not support provider tool type %q", d.subject, kind)
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
		return nil, fmt.Errorf("%s requires an array of conversation items", d.subject)
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
	if noHostedSearch {
		instructions += policy.noHostedSearchInstruction
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
				return nil, fmt.Errorf("unsupported %s message role %q", d.name, role)
			}
			content, err := providerContent(item["content"], d)
			if err != nil {
				return nil, err
			}
			// Streamed reasoning precedes its response's text, so the
			// assistant it opened receives that text.
			if last := len(messages) - 1; role == "assistant" && last >= 0 && messages[last].role == "assistant" &&
				messages[last].content == nil && len(messages[last].calls) == 0 {
				messages[last].content = content
				continue
			}
			messages = append(messages, providerMessage{role: role, content: content})
		case "agent_message":
			content, err := providerContent(item["content"], d)
			if err != nil {
				return nil, err
			}
			messages = append(messages, providerMessage{role: "user", content: content})
		case "function_call", "custom_tool_call":
			name := providerToolName(jsonString(item, "namespace"), jsonString(item, "name"))
			arguments := jsonString(item, "arguments")
			if kind == "custom_tool_call" {
				var input *string
				if json.Unmarshal(item["input"], &input) != nil || input == nil {
					return nil, fmt.Errorf("%s custom tool history input is not a string", d.subject)
				}
				arguments = string(mustMarshalJSON(map[string]string{"input": *input}))
			}
			if !json.Valid([]byte(arguments)) {
				return nil, fmt.Errorf("invalid JSON in %s tool-call history", d.name)
			}
			call := providerCall{id: jsonString(item, "call_id"), name: name, arguments: arguments}
			if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
				messages = append(messages, providerMessage{role: "assistant"})
			}
			last := &messages[len(messages)-1]
			last.calls = append(last.calls, call)
		case "function_call_output", "custom_tool_call_output":
			content, err := providerContent(item["output"], d)
			if err != nil {
				return nil, err
			}
			messages = append(messages, providerMessage{role: "tool", callID: jsonString(item, "call_id"), content: content})
		case "reasoning":
			if policy.restoreReasoning != nil && jsonString(item, "encrypted_content") != "" {
				restored, err := policy.restoreReasoning(jsonString(item, "encrypted_content"))
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
				return nil, incompatibleRequest(d.prefix+"_encrypted_history", fmt.Sprintf("%s cannot read encrypted OpenAI reasoning; start a fresh %s thread or spawn with fork_turns=none.", d.name, d.name))
			}
			if policy.replaySummaries {
				var summary []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(item["summary"], &summary); err != nil {
					return nil, fmt.Errorf("invalid %s reasoning history", d.name)
				}
				var reasoning strings.Builder
				for _, part := range summary {
					if part.Type != "summary_text" {
						return nil, fmt.Errorf("unsupported %s reasoning history", d.name)
					}
					reasoning.WriteString(part.Text)
				}
				if reasoning.Len() > 0 {
					if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
						messages = append(messages, providerMessage{role: "assistant"})
					}
					// Reasoning resumed after text is a second item; keep both.
					messages[len(messages)-1].reasoning += reasoning.String()
				}
			}
			// Unencrypted reasoning summaries are explanatory metadata, not messages.
		default:
			return nil, fmt.Errorf("%s cannot translate history item type %q", d.subject, kind)
		}
	}

	options, err := validateProviderOptions(request.fields, tr.tools, policy)
	if err != nil {
		return nil, err
	}
	tr.body, err = endpoint.encode(model, messages, tools, options, d)
	if err != nil {
		return nil, err
	}

	return tr, nil
}

// Source: internal/router/grok_protocol.go:479:516@413bbaa9b9202042199c753690febe7bc1358fa6 grokContent
func providerContent(raw json.RawMessage, d providerDiagnostics) (any, error) {
	var text *string
	if json.Unmarshal(raw, &text) == nil && text != nil {
		return *text, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || parts == nil {
		return nil, fmt.Errorf("unsupported %s message content", d.name)
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
				return nil, fmt.Errorf("%s requires an image URL or data URL, not a provider file ID", d.subject)
			}
			image := map[string]string{"url": imageURL}
			if detail := jsonString(part, "detail"); detail != "" {
				image["detail"] = detail
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": image})
		case "refusal":
			content = append(content, map[string]any{"type": "refusal", "refusal": jsonString(part, "refusal")})
		case "encrypted_content":
			return nil, incompatibleRequest(d.prefix+"_encrypted_history", fmt.Sprintf("%s cannot read encrypted agent messages; start a fresh thread with the %s collaboration bridge enabled.", d.name, d.name))
		default:
			return nil, fmt.Errorf("unsupported %s content type %q", d.name, jsonString(part, "type"))
		}
	}
	return content, nil
}
