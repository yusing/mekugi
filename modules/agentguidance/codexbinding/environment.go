package codexbinding

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"html"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/environment"
)

const environmentStart = "<mekugi_environment_facts>"
const environmentEnd = "</mekugi_environment_facts>"

func projectEnvironment(ctx context.Context, input binding.InstructionInput, admitted binding.EnvironmentContext, store *environment.Store) (binding.InstructionUpdate, error) {
	result := binding.InstructionUpdate{InstructionInput: input}
	text, ok := responses.DecodeString(input.Instructions)
	if !ok && len(input.Instructions) != 0 && !bytes.Equal(bytes.TrimSpace(input.Instructions), []byte("null")) {
		return result, errors.New("base instructions must be a string or null")
	}
	updated, err := removeEnvironment(text)
	if err != nil {
		return result, err
	}
	if updated != text {
		result.Instructions = responses.MustMarshalJSON(updated)
		result.InstructionsChanged = true
	}
	rawInput := input.Input
	if len(rawInput) == 0 {
		rawInput = []byte("null")
	}
	decoded, err := responses.DecodeInput(rawInput)
	if err != nil {
		return result, err
	}
	items := decoded.Items
	if bytes.Equal(bytes.TrimSpace(rawInput), []byte("null")) {
		items = nil
	} else if decoded.Text != nil {
		items = []jsonv1.RawMessage{responses.MustMarshalJSON(map[string]string{"role": "user", "content": *decoded.Text})}
	} else if items == nil {
		return result, errors.New("environment projection requires string or array input")
	}
	projected := make([]jsonv1.RawMessage, 0, len(items)+1)
	boundary := -1
	for _, raw := range items {
		item, ok := responses.DecodeItem(raw)
		if ok && (item.Type == "" || item.Type == "message") {
			if item.Role == "developer" {
				content, changed, err := removeEnvironmentContent(item.Content)
				if err != nil {
					return result, err
				}
				if changed {
					if content == nil {
						continue
					}
					item.SetContent(content)
					raw = responses.MustMarshalJSON(item)
				}
			}
			if item.Role == "user" && boundary < 0 {
				boundary = len(projected)
			}
		}
		projected = append(projected, raw)
	}
	if boundary < 0 {
		boundary = len(projected)
		for index, raw := range projected {
			item, ok := responses.DecodeItem(raw)
			if !ok || item.Type != "additional_tools" && item.Role != "developer" && item.Role != "system" {
				boundary = index
				break
			}
		}
	}
	facts := store.Snapshot(ctx, environment.Context{Session: admitted.Session, Thread: admitted.Thread, Workspace: admitted.Workspace, Revision: admitted.Revision}, admitted.PreviousRevision)
	body := html.EscapeString(facts.Text(admitted.Workspace))
	section := environmentStart + "\nCaptured at context start and refreshed after compaction or workspace change: use them rather than re-probing; re-check only facts later work may have changed.\n" + body + "\n" + environmentEnd
	// Keep mutable facts after the static prefix, without changing native history
	// or splitting any tool/result sequence. Inherited projections are replaced.
	projected = append(projected, nil)
	copy(projected[boundary+1:], projected[boundary:])
	projected[boundary] = responses.MustMarshalJSON(map[string]string{"type": "message", "role": "developer", "content": section})
	encoded := responses.MustMarshalJSON(projected)
	if !bytes.Equal(encoded, input.Input) {
		result.Input, result.InputChanged = encoded, true
	}
	return result, nil
}

func removeEnvironment(text string) (string, error) {
	updated, err := replaceSection(text, "", environmentStart, environmentEnd, true, false)
	// Earlier projections appended the owned section with this separator,
	// possibly followed by another owner's subsequently appended instructions.
	start := strings.Index(text, environmentStart)
	if err == nil && start >= 2 && text[start-2:start] == "\n\n" {
		updated = updated[:start-2] + updated[start:]
	}
	return updated, err
}

func removeEnvironmentContent(raw jsonv1.RawMessage) (jsonv1.RawMessage, bool, error) {
	var projectionErr error
	updated, changed, err := transformResponsesTextContent(raw, func(text string) string {
		value, err := removeEnvironment(text)
		if err != nil {
			projectionErr = err
			return text
		}
		return value
	}, isResponsesInputTextPart)
	if err != nil || projectionErr != nil {
		return nil, false, errors.Join(err, projectionErr)
	}
	if !changed {
		return raw, false, nil
	}
	if text, ok := responses.DecodeString(updated); ok {
		if text == "" {
			return nil, true, nil
		}
		return updated, true, nil
	}
	var parts []jsonv1.RawMessage
	if err := json.Unmarshal(updated, &parts); err != nil {
		return nil, false, err
	}
	for _, part := range parts {
		var fields map[string]jsonv1.RawMessage
		if err := json.Unmarshal(part, &fields); err != nil || !isResponsesInputTextPart(responses.StringField(fields, "type")) || responses.StringField(fields, "text") != "" {
			return updated, true, nil
		}
	}
	return nil, true, nil
}
