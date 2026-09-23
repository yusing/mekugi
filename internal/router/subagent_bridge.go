package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const subagentBridgeNamespace = "mekugi_collaboration"

// subagentBridge projects the provider-reserved collaboration schema into an
// ordinary plaintext namespace. Codex still executes every restored native call.
// An explicitly empty encrypted_function_args is significant to Codex: without
// it, even a plaintext message is delivered as opaque encrypted_content.
type subagentBridge struct {
	names map[string]bool
}

func prepareSubagentBridge(request *parsedResponsesRequest, grokEnabled bool, openCode ...string) (*subagentBridge, error) {
	openCodeNote := ""
	if len(openCode) > 0 {
		openCodeNote = " " + strings.ReplaceAll(embeddedInstruction("opencode_spawn"), "%MODELS%", strings.Join(openCode, ", "))
	}
	bridge := &subagentBridge{names: make(map[string]bool)}
	var projectTools func(json.RawMessage) (json.RawMessage, error)
	projectTools = func(raw json.RawMessage) (json.RawMessage, error) {
		var tools []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tools); err != nil {
			return nil, fmt.Errorf("decode collaboration tools: %w", err)
		}
		for _, tool := range tools {
			if jsonString(tool, "type") != "namespace" {
				continue
			}
			switch jsonString(tool, "name") {
			case subagentBridgeNamespace:
				return nil, errors.New("tool namespace mekugi_collaboration is reserved by the collaboration bridge")
			case "collaboration":
				namespaceDescription := strings.TrimSpace(jsonString(tool, "description") + "\n" + embeddedInstruction("collaboration_namespace"))
				tool["description"] = mustMarshalJSON(namespaceDescription)
				var functions []map[string]json.RawMessage
				if err := json.Unmarshal(tool["tools"], &functions); err != nil {
					return nil, err
				}
				for _, fn := range functions {
					bridge.names[jsonString(fn, "name")] = true
					if openCodeNote != "" && jsonString(fn, "name") == "spawn_agent" {
						fn["description"] = mustMarshalJSON(jsonString(fn, "description") + openCodeNote)
					}
					// Remove only the provider's message encryption annotation, not arbitrary
					// schema keywords or strictness owned by the collaboration runtime.
					if raw, ok := fn["parameters"]; ok {
						var schema map[string]json.RawMessage
						if err := json.Unmarshal(raw, &schema); err != nil {
							return nil, err
						}
						var properties map[string]json.RawMessage
						if err := json.Unmarshal(schema["properties"], &properties); err != nil {
							return nil, err
						}
						if raw, ok := properties["message"]; ok {
							var message map[string]json.RawMessage
							if err := json.Unmarshal(raw, &message); err != nil {
								return nil, err
							}
							delete(message, "encrypted")
							properties["message"] = mustMarshalJSON(message)
						}
						if grokEnabled && jsonString(fn, "name") == "spawn_agent" {
							for name, note := range map[string]string{
								"model":            embeddedInstruction("grok_model"),
								"fork_turns":       embeddedInstruction("grok_fork_turns"),
								"reasoning_effort": embeddedInstruction("grok_reasoning_effort"),
							} {
								raw, exists := properties[name]
								if !exists {
									continue
								}
								var property map[string]json.RawMessage
								if err := json.Unmarshal(raw, &property); err != nil {
									return nil, err
								}
								if property == nil {
									continue
								}
								property["description"] = mustMarshalJSON(strings.TrimSpace(jsonString(property, "description") + "\n" + note))
								properties[name] = mustMarshalJSON(property)
							}
						}
						schema["properties"] = mustMarshalJSON(properties)
						fn["parameters"] = mustMarshalJSON(schema)
					}
					if description := jsonString(fn, "description"); description != "" {
						fn["description"] = mustMarshalJSON(strings.ReplaceAll(description, "collaboration.", subagentBridgeNamespace+"."))
					}
				}
				tool["name"] = mustMarshalJSON(subagentBridgeNamespace)
				tool["tools"] = mustMarshalJSON(functions)
			default:
				if nested, ok := tool["tools"]; ok {
					projected, err := projectTools(nested)
					if err != nil {
						return nil, err
					}
					tool["tools"] = projected
				}
			}
		}
		return json.Marshal(tools)
	}
	if raw, ok := request.fields["tools"]; ok {
		projected, err := projectTools(raw)
		if err != nil {
			return nil, err
		}
		request.fields["tools"] = projected
	}
	var input []map[string]json.RawMessage
	var scalarInput string
	if raw, ok := request.fields["input"]; ok && json.Unmarshal(raw, &scalarInput) != nil {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, fmt.Errorf("decode collaboration history: %w", err)
		}
		for _, item := range input {
			switch jsonString(item, "type") {
			case "additional_tools":
				projected, err := projectTools(item["tools"])
				if err != nil {
					return nil, err
				}
				item["tools"] = projected
			case "function_call":
				if jsonString(item, "namespace") == "collaboration" {
					if raw, present := item["encrypted_function_args"]; present {
						var encrypted []string
						if err := json.Unmarshal(raw, &encrypted); err != nil {
							return nil, fmt.Errorf("decode collaboration encryption marker: %w", err)
						}
						if len(encrypted) != 0 {
							// Old native calls retain their encryption metadata and
							// identity. They were not produced by this plaintext bridge.
							continue
						}
					}
					item["namespace"] = mustMarshalJSON(subagentBridgeNamespace)
					delete(item, "encrypted_function_args")
				}
			}
		}
		request.fields["input"] = mustMarshalJSON(input)
	}
	request.toolCatalog = nil
	if len(bridge.names) == 0 {
		return nil, nil
	}
	if input != nil {
		request.setInput(mustMarshalJSON(input))
	}
	if raw, ok := request.fields["tool_choice"]; ok {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) == nil && jsonString(choice, "namespace") == "collaboration" {
			choice["namespace"] = mustMarshalJSON(subagentBridgeNamespace)
			request.fields["tool_choice"] = mustMarshalJSON(choice)
		}
	}
	return bridge, nil
}

func (b *subagentBridge) restoreItem(raw json.RawMessage) (json.RawMessage, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	if jsonString(item, "type") != "function_call" || jsonString(item, "namespace") != subagentBridgeNamespace {
		return raw, nil
	}
	name := jsonString(item, "name")
	if !b.names[name] {
		return nil, errors.New("provider returned an unavailable collaboration bridge tool")
	}
	item["namespace"] = mustMarshalJSON("collaboration")
	switch name {
	case "spawn_agent", "send_message", "followup_task":
		item["encrypted_function_args"] = json.RawMessage(`[]`)
	}
	return marshalProtocolJSON(item)
}

func (b *subagentBridge) TransformJSON(payload []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	if raw, ok := response["output"]; ok {
		var output []json.RawMessage
		if err := json.Unmarshal(raw, &output); err != nil {
			return nil, err
		}
		for i, item := range output {
			restored, err := b.restoreItem(item)
			if err != nil {
				return nil, err
			}
			output[i] = restored
		}
		response["output"] = mustMarshalJSON(output)
	}
	return marshalProtocolJSON(response)
}
func (b *subagentBridge) TransformSSE(payload []byte) ([][]byte, error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}
	if raw, ok := event["item"]; ok {
		restored, err := b.restoreItem(raw)
		if err != nil {
			return nil, err
		}
		event["item"] = restored
	}
	if raw, ok := event["response"]; ok {
		restored, err := b.TransformJSON(raw)
		if err != nil {
			return nil, err
		}
		event["response"] = restored
	}
	return [][]byte{mustMarshalJSON(event)}, nil
}
func (*subagentBridge) Finish(bool) error { return nil }
