package responses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/provider_endpoint.go:1:144@5a2082938e797e6e15c374eb6ae135a0753dea24

type providerOptions struct {
	choice     string
	namedTool  string
	parallel   json.RawMessage
	effort     string
	maxOutput  json.RawMessage
	fields     map[string]json.RawMessage
	textFormat map[string]json.RawMessage
}

// Source: internal/router/grok_protocol.go:339:468@413bbaa9b9202042199c753690febe7bc1358fa6 translateChatRequest
func validateProviderOptions(fields map[string]json.RawMessage, tools map[string]providerTool, policy Policy) (providerOptions, error) {
	d := policy.Diagnostics
	o := providerOptions{fields: fields}
	if raw, ok := fields["tool_choice"]; ok && len(tools) > 0 {
		if json.Unmarshal(raw, &o.choice) != nil {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return o, fmt.Errorf("unsupported %s tool choice", d.Name)
			}
			o.namedTool = providerToolName(responses.StringField(obj, "namespace"), responses.StringField(obj, "name"))
			if _, ok := tools[o.namedTool]; !ok {
				return o, fmt.Errorf("%s tool choice references an unavailable tool", d.Subject)
			}
		}
	}
	if len(tools) > 0 {
		o.parallel = fields["parallel_tool_calls"]
	}
	if raw, ok := fields["reasoning"]; ok {
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(raw, &reasoning) != nil {
			return o, fmt.Errorf("invalid %s reasoning settings", d.Name)
		}
		var err error
		o.effort, err = policy.ResolveEffort(reasoning.Effort)
		if err != nil {
			return o, err
		}
	}
	if raw, ok := fields["max_output_tokens"]; ok && strings.TrimSpace(string(raw)) != "null" {
		o.maxOutput = raw
	}
	if raw, ok := fields["text"]; ok {
		var text map[string]json.RawMessage
		if err := json.Unmarshal(raw, &text); err != nil {
			return o, err
		}
		if raw, ok := text["format"]; ok {
			if err := json.Unmarshal(raw, &o.textFormat); err != nil {
				return o, err
			}
			switch responses.StringField(o.textFormat, "type") {
			case "", "text", "json_object", "json_schema":
			default:
				return o, fmt.Errorf("unsupported %s structured output format", d.Name)
			}
		}
	}
	return o, nil
}

func providerRequestBody(model string, o providerOptions) map[string]any {
	body := map[string]any{"model": model, "stream": true}
	for _, field := range []string{"temperature", "top_p", "service_tier"} {
		if raw, ok := o.fields[field]; ok {
			body[field] = raw
		}
	}
	return body
}

func rejectUnsignedReasoning(message providerMessage, d Diagnostics) error {
	if message.reasoning != "" {
		return d.Incompatible(d.Prefix+"_encrypted_history", fmt.Sprintf("Changing %s API formats requires a fresh thread; unsigned reasoning cannot replace provider replay data.", d.Name))
	}
	return nil
}

// providerMessage retains validated history independently of any endpoint's
// request envelope. Only the final builder groups it into wire messages.
type providerMessage struct {
	role      string
	content   any
	callID    string
	calls     []providerCall
	retained  []any
	reasoning string
}

type providerCall struct {
	id, name, arguments string
}

func endpointContent(value any, render func(map[string]any) (map[string]any, error), d Diagnostics) ([]any, error) {
	var parts []any
	switch value := value.(type) {
	case nil:
		return parts, nil
	case string:
		if value != "" {
			parts = []any{map[string]any{"type": "text", "text": value}}
		}
	case []any:
		parts = value
	default:
		return nil, fmt.Errorf("unsupported %s message content", d.Name)
	}
	var result []any
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			text, ok := raw.(map[string]string)
			if !ok {
				return nil, fmt.Errorf("unsupported %s content part", d.Name)
			}
			part = map[string]any{"type": text["type"], "text": text["text"]}
		}
		next, err := render(part)
		if err != nil {
			return nil, err
		}
		result = append(result, next)
	}
	return result, nil
}
