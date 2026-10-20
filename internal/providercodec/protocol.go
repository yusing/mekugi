package providercodec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/yusing/mekugi/internal/responses"
)

// Source: internal/router/provider_protocol.go:1:324@5a2082938e797e6e15c374eb6ae135a0753dea24

// Endpoint adapters encode validated request data and normalize their own stream
// framing. They do not select a provider or emit the Responses lifecycle.
type providerEndpoint interface {
	encode(string, []providerMessage, []map[string]any, providerOptions, Diagnostics) (map[string]any, error)
	readStream(io.Reader, Diagnostics, func(providerChunk) error) error
}

type providerTool struct{ name, namespace, kind string }

// Stream retains validated native tool identities for one translated request.
// Endpoint framing and normalized chunks remain private to the codec.
type Stream struct {
	policy   Policy
	endpoint providerEndpoint
	model    string
	tools    map[string]providerTool
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

// Codex advertises exec wait's integer fields as "number", but its native
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
		responses.StringField(properties["cell_id"], "type") != "string" {
		return parameters
	}
	changed := false
	for _, field := range []string{"yield_time_ms", "max_tokens"} {
		if responses.StringField(properties[field], "type") == "number" {
			properties[field]["type"] = json.RawMessage(`"integer"`)
			changed = true
		}
	}
	if !changed {
		return parameters
	}
	schema["properties"] = responses.MustMarshalJSON(properties)
	return responses.MustMarshalJSON(schema)
}

// Translate validates shared tool identity and history, then builds
// the selected provider's endpoint request without an intermediate Chat request.
// It translates only representations with defined equivalents, never treating
// encrypted_content as text or silently removing unsupported history items.
// The caller remains responsible for executing tools.
// Source: internal/router/grok_protocol.go:81:477@413bbaa9b9202042199c753690febe7bc1358fa6 translateChatRequest
func Translate(fields map[string]json.RawMessage, model, format string, policy Policy) (map[string]any, *Stream, error) {
	var endpoint providerEndpoint
	switch format {
	case "chat":
		endpoint = chatEndpoint{}
	case "anthropic":
		endpoint = messagesEndpoint{}
	case "responses":
		endpoint = responsesEndpoint{}
	default:
		return nil, nil, fmt.Errorf("unsupported provider API format %q", format)
	}
	d := policy.Diagnostics
	if value := responses.StringField(fields, "previous_response_id"); value != "" {
		return nil, nil, fmt.Errorf("%s requires explicit conversation history, not previous_response_id", d.Subject)
	}
	tr := &Stream{policy: policy, endpoint: endpoint, tools: make(map[string]providerTool), model: model}
	noHostedSearch := false
	var tools []map[string]any
	var addTools func(json.RawMessage, string) error
	addTools = func(raw json.RawMessage, namespace string) error {
		var defs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &defs); err != nil {
			return fmt.Errorf("decode %s tools: %w", d.Name, err)
		}
		for _, def := range defs {
			kind, name := responses.StringField(def, "type"), responses.StringField(def, "name")
			if kind == "namespace" {
				if namespace != "" {
					return fmt.Errorf("nested tool namespaces are not supported by %s", d.Name)
				}
				if err := addTools(def["tools"], name); err != nil {
					return err
				}
				continue
			}
			switch kind {
			case "function", "custom":
				if name == "" {
					return fmt.Errorf("%s tool has no name", d.Subject)
				}
				wireName := providerToolName(namespace, name)
				if _, exists := tr.tools[wireName]; exists {
					return fmt.Errorf("duplicate %s tool identity", d.Name)
				}
				tr.tools[wireName] = providerTool{name: name, namespace: namespace, kind: kind}
				fn := map[string]any{"name": wireName, "description": responses.StringField(def, "description")}
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
					fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string", "description": policy.CustomInputInstruction}}, "required": []string{"input"}, "additionalProperties": false}
					if namespace == "" && name == "exec" {
						// Codex's description leaves implicit that unprinted nested
						// results never reach the model; other models miss it.
						fn["description"] = fn["description"].(string) + "\n" + policy.ExecInstruction
					}
					if format, ok := def["format"]; ok {
						fn["description"] = fn["description"].(string) + "\n" + policy.FormatInstructionPrefix + " " + string(format)
					}
				}
				tools = append(tools, fn)
			case "web_search", "web_search_preview":
				// Provider-hosted search cannot run inside Codex. Do not advertise an
				// unavailable executor; explicitly describe this capability difference.
				noHostedSearch = true
			default:
				return fmt.Errorf("%s does not support provider tool type %q", d.Subject, kind)
			}
		}
		return nil
	}
	if raw, ok := fields["tools"]; ok {
		if err := addTools(raw, ""); err != nil {
			return nil, nil, err
		}
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(fields["input"], &input); err != nil {
		return nil, nil, fmt.Errorf("%s requires an array of conversation items", d.Subject)
	}
	for _, item := range input {
		if responses.StringField(item, "type") == "additional_tools" {
			if err := addTools(item["tools"], ""); err != nil {
				return nil, nil, err
			}
		}
	}
	messages := []providerMessage{}
	instructions := responses.StringField(fields, "instructions")
	if noHostedSearch {
		instructions += policy.NoHostedSearchInstruction
	}
	if instructions != "" {
		messages = append(messages, providerMessage{role: "system", content: instructions})
	}
	for _, item := range input {
		kind := responses.StringField(item, "type")
		switch kind {
		case "additional_tools":
			continue
		case "message", "":
			role := responses.StringField(item, "role")
			if role == "developer" {
				role = "system"
			}
			switch role {
			case "system", "user", "assistant":
			default:
				return nil, nil, fmt.Errorf("unsupported %s message role %q", d.Name, role)
			}
			content, err := providerContent(item["content"], d)
			if err != nil {
				return nil, nil, err
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
				return nil, nil, err
			}
			messages = append(messages, providerMessage{role: "user", content: content})
		case "function_call", "custom_tool_call":
			name := providerToolName(responses.StringField(item, "namespace"), responses.StringField(item, "name"))
			arguments := responses.StringField(item, "arguments")
			if kind == "custom_tool_call" {
				var input *string
				if json.Unmarshal(item["input"], &input) != nil || input == nil {
					return nil, nil, fmt.Errorf("%s custom tool history input is not a string", d.Subject)
				}
				arguments = string(responses.MustMarshalJSON(map[string]string{"input": *input}))
			}
			if !json.Valid([]byte(arguments)) {
				return nil, nil, fmt.Errorf("invalid JSON in %s tool-call history", d.Name)
			}
			call := providerCall{id: responses.StringField(item, "call_id"), name: name, arguments: arguments}
			if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
				messages = append(messages, providerMessage{role: "assistant"})
			}
			last := &messages[len(messages)-1]
			last.calls = append(last.calls, call)
		case "function_call_output", "custom_tool_call_output":
			content, err := providerContent(item["output"], d)
			if err != nil {
				return nil, nil, err
			}
			messages = append(messages, providerMessage{role: "tool", callID: responses.StringField(item, "call_id"), content: content})
		case "reasoning":
			if policy.RestoreReasoning != nil && responses.StringField(item, "encrypted_content") != "" {
				restored, err := policy.RestoreReasoning(responses.StringField(item, "encrypted_content"))
				if err != nil {
					return nil, nil, err
				}
				if len(messages) == 0 || messages[len(messages)-1].role != "assistant" {
					messages = append(messages, providerMessage{role: "assistant"})
				}
				last := &messages[len(messages)-1]
				last.retained = append(last.retained, restored)
				continue
			}
			if encrypted := responses.StringField(item, "encrypted_content"); encrypted != "" {
				return nil, nil, d.Incompatible(d.Prefix+"_encrypted_history", fmt.Sprintf("%s cannot read encrypted OpenAI reasoning; start a fresh %s thread or spawn with fork_turns=none.", d.Name, d.Name))
			}
			if policy.ReplaySummaries {
				var summary []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(item["summary"], &summary); err != nil {
					return nil, nil, fmt.Errorf("invalid %s reasoning history", d.Name)
				}
				var reasoning strings.Builder
				for _, part := range summary {
					if part.Type != "summary_text" {
						return nil, nil, fmt.Errorf("unsupported %s reasoning history", d.Name)
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
			return nil, nil, fmt.Errorf("%s cannot translate history item type %q", d.Subject, kind)
		}
	}

	options, err := validateProviderOptions(fields, tr.tools, policy)
	if err != nil {
		return nil, nil, err
	}
	body, err := endpoint.encode(model, messages, tools, options, d)
	if err != nil {
		return nil, nil, err
	}

	return body, tr, nil
}

// Source: internal/router/grok_protocol.go:479:516@413bbaa9b9202042199c753690febe7bc1358fa6 grokContent
func providerContent(raw json.RawMessage, d Diagnostics) (any, error) {
	var text *string
	if json.Unmarshal(raw, &text) == nil && text != nil {
		return *text, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || parts == nil {
		return nil, fmt.Errorf("unsupported %s message content", d.Name)
	}
	if len(parts) == 0 {
		return "", nil
	}
	var content []any
	for _, part := range parts {
		switch responses.StringField(part, "type") {
		case "input_text", "output_text", "text":
			value := responses.StringField(part, "text")
			content = append(content, map[string]string{"type": "text", "text": value})
		case "input_image":
			imageURL := responses.StringField(part, "image_url")
			if imageURL == "" {
				return nil, fmt.Errorf("%s requires an image URL or data URL, not a provider file ID", d.Subject)
			}
			image := map[string]string{"url": imageURL}
			if detail := responses.StringField(part, "detail"); detail != "" {
				image["detail"] = detail
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": image})
		case "refusal":
			content = append(content, map[string]any{"type": "refusal", "refusal": responses.StringField(part, "refusal")})
		case "encrypted_content":
			return nil, d.Incompatible(d.Prefix+"_encrypted_history", fmt.Sprintf("%s cannot read encrypted agent messages; start a fresh thread with the %s collaboration bridge enabled.", d.Name, d.Name))
		default:
			return nil, fmt.Errorf("unsupported %s content type %q", d.Name, responses.StringField(part, "type"))
		}
	}
	return content, nil
}
