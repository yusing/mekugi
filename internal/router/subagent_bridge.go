package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/responses"
)

const subagentBridgeNamespace = "mekugi_collaboration"

// subagentBridge projects the provider-reserved collaboration schema into an
// ordinary plaintext namespace. Codex still executes every restored native call.
// An explicitly empty encrypted_function_args is significant to Codex: without
// it, even a plaintext message is delivered as opaque encrypted_content.
type subagentBridge struct {
	names map[string]bool
}

func prepareSubagentBridge(request *parsedResponsesRequest, grokEnabled, grokUnprefixed bool, openCode ...string) (*subagentBridge, error) {
	grokModelNote := strings.ReplaceAll(embeddedInstruction("grok_model"), "%MODELS%", "`grok:"+strings.Join(grokModels, "`, `grok:")+"`")
	if grokUnprefixed {
		grokModelNote = strings.ReplaceAll(grokModelNote, "grok:", "")
	}
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
			if responses.StringField(tool, "type") != "namespace" {
				continue
			}
			switch responses.StringField(tool, "name") {
			case subagentBridgeNamespace:
				return nil, errors.New("tool namespace mekugi_collaboration is reserved by the collaboration bridge")
			case "collaboration":
				namespaceDescription := strings.TrimSpace(responses.StringField(tool, "description") + "\n" + embeddedInstruction("collaboration_namespace"))
				tool["description"] = responses.MustMarshalJSON(namespaceDescription)
				var functions []map[string]json.RawMessage
				if err := json.Unmarshal(tool["tools"], &functions); err != nil {
					return nil, err
				}
				for _, fn := range functions {
					bridge.names[responses.StringField(fn, "name")] = true
					if openCodeNote != "" && responses.StringField(fn, "name") == "spawn_agent" {
						fn["description"] = responses.MustMarshalJSON(responses.StringField(fn, "description") + openCodeNote)
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
							properties["message"] = responses.MustMarshalJSON(message)
						}
						if grokEnabled && responses.StringField(fn, "name") == "spawn_agent" {
							for name, note := range map[string]string{
								"model":            grokModelNote,
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
								property["description"] = responses.MustMarshalJSON(strings.TrimSpace(responses.StringField(property, "description") + "\n" + note))
								properties[name] = responses.MustMarshalJSON(property)
							}
						}
						schema["properties"] = responses.MustMarshalJSON(properties)
						fn["parameters"] = responses.MustMarshalJSON(schema)
					}
					if description := responses.StringField(fn, "description"); description != "" {
						fn["description"] = responses.MustMarshalJSON(strings.ReplaceAll(description, "collaboration.", subagentBridgeNamespace+"."))
					}
				}
				tool["name"] = responses.MustMarshalJSON(subagentBridgeNamespace)
				tool["tools"] = responses.MustMarshalJSON(functions)
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
			switch responses.StringField(item, "type") {
			case "additional_tools":
				projected, err := projectTools(item["tools"])
				if err != nil {
					return nil, err
				}
				item["tools"] = projected
			case "function_call":
				if responses.StringField(item, "namespace") == "collaboration" {
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
					item["namespace"] = responses.MustMarshalJSON(subagentBridgeNamespace)
					delete(item, "encrypted_function_args")
				}
			}
		}
		request.fields["input"] = responses.MustMarshalJSON(input)
	}
	request.toolCatalog = nil
	if len(bridge.names) == 0 {
		return nil, nil
	}
	if input != nil {
		request.setInput(responses.MustMarshalJSON(input))
	}
	if raw, ok := request.fields["tool_choice"]; ok {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) == nil && responses.StringField(choice, "namespace") == "collaboration" {
			choice["namespace"] = responses.MustMarshalJSON(subagentBridgeNamespace)
			request.fields["tool_choice"] = responses.MustMarshalJSON(choice)
		}
	}
	return bridge, nil
}

func (b *subagentBridge) restoreItem(raw json.RawMessage) (json.RawMessage, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	if responses.StringField(item, "type") != "function_call" || responses.StringField(item, "namespace") != subagentBridgeNamespace {
		return raw, nil
	}
	name := responses.StringField(item, "name")
	if !b.names[name] {
		return nil, errors.New("provider returned an unavailable collaboration bridge tool")
	}
	item["namespace"] = responses.MustMarshalJSON("collaboration")
	switch name {
	case "spawn_agent", "send_message", "followup_task":
		item["encrypted_function_args"] = json.RawMessage(`[]`)
	}
	return responses.MarshalJSON(item)
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
		response["output"] = responses.MustMarshalJSON(output)
	}
	return responses.MarshalJSON(response)
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
	return [][]byte{responses.MustMarshalJSON(event)}, nil
}
func (*subagentBridge) Finish(bool) error { return nil }
