package codexbinding_test

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/modules/agentguidance/codexbinding"
)

const nativeThreadInstructions = `{
	"type":"message", "id":"native-policy", "role":"developer",
	"content":[
		{"type":"input_text","text":"<multi_agent_role>Native role.</multi_agent_role>"},
		{"type":"input_text","text":"Keep native collaboration mode."},
		{"type":"input_text","text":"Native usage hint."},
		{"type":"input_text","text":"<multi_agent_role>Native role.</multi_agent_role>"},
		{"type":"input_image","image_url":"https://example.invalid/image"}
	],
	"internal_chat_message_metadata_passthrough":{"content_item_kinds":[
		"multi_agent.role_instructions","multi_agent.mode_instructions","multi_agent.usage_hint",null,null
	]}
}`

func encodeThreadFixture(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func threadProjectionFixture(t *testing.T) (map[string]any, []guidance.Replacement) {
	t.Helper()
	fragments, err := binding.NativeInstructionFragments("admitted-thread", jsontext.Value(nativeThreadInstructions))
	if err != nil || len(fragments) != 3 {
		t.Fatalf("native fragments = %+v, %v", fragments, err)
	}
	var replacements []guidance.Replacement
	for _, fragment := range fragments {
		switch fragment.Kind {
		case "multi_agent.role_instructions":
			replacements = append(replacements, guidance.Replacement{Fragment: fragment, Text: "Projected logical role."})
		case "multi_agent.usage_hint":
			replacements = append(replacements, guidance.Replacement{Fragment: fragment, Text: ""})
		}
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(nativeThreadInstructions), &item); err != nil {
		t.Fatal(err)
	}
	// Provider requests omit the native host metadata. Identity and bytes must
	// still match the separately admitted fragments.
	delete(item, "internal_chat_message_metadata_passthrough")
	return item, replacements
}

func TestThreadGuidanceProjectsAdmittedParts(t *testing.T) {
	item, replacements := threadProjectionFixture(t)
	lookalike := map[string]any{
		"type": "message", "id": "custom-policy", "role": "developer",
		"content": []any{map[string]any{"type": "input_text", "text": "<multi_agent_role>Native role.</multi_agent_role>"}},
	}
	input := binding.InstructionInput{
		Instructions: []byte(`"Keep base authority."`),
		Input:        encodeThreadFixture(t, []any{item, lookalike}),
	}
	original := bytes.Clone(input.Input)
	project := codexbinding.Codex.Guidance(nil).Threads
	if project == nil {
		t.Fatal("Codex guidance did not expose thread projection")
	}
	got, err := project(input, replacements)
	if err != nil {
		t.Fatal(err)
	}
	if !got.InputChanged || got.InstructionsChanged || !bytes.Equal(got.Instructions, input.Instructions) {
		t.Fatalf("incorrect change reporting or base instructions: %+v", got)
	}
	var decoded any
	if err := json.Unmarshal(got.Input, &decoded); err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal([]byte(`[
		{"type":"message","id":"native-policy","role":"developer","content":[
			{"type":"input_text","text":"Projected logical role."},
			{"type":"input_text","text":"Keep native collaboration mode."},
			{"type":"input_text","text":""},
			{"type":"input_text","text":"<multi_agent_role>Native role.</multi_agent_role>"},
			{"type":"input_image","image_url":"https://example.invalid/image"}
		]},
		{"type":"message","id":"custom-policy","role":"developer","content":[
			{"type":"input_text","text":"<multi_agent_role>Native role.</multi_agent_role>"}
		]}
	]`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("projected input = %s", got.Input)
	}
	if !bytes.Equal(input.Input, original) {
		t.Fatal("projection mutated the caller's request")
	}
	again, err := project(got.InstructionInput, replacements)
	if err != nil || again.InputChanged || again.InstructionsChanged || !bytes.Equal(again.Input, got.Input) || !bytes.Equal(again.Instructions, input.Instructions) {
		t.Fatalf("projection is not idempotent: %+v, %v", again, err)
	}
}

func TestThreadGuidanceRequiresAdmittedIdentityAndContent(t *testing.T) {
	for _, name := range []string{"different item", "user role", "changed bytes", "changed source text"} {
		t.Run(name, func(t *testing.T) {
			item, replacements := threadProjectionFixture(t)
			wantError := false
			switch name {
			case "different item":
				item["id"] = "unrelated-policy"
			case "user role":
				item["role"] = "user"
			case "changed bytes":
				item["content"].([]any)[0].(map[string]any)["text"] = "Conflicting policy."
				wantError = true
			case "changed source text":
				replacements[0].Fragment.Text = "Conflicting retained source."
				wantError = true
			}
			input := binding.InstructionInput{Input: encodeThreadFixture(t, []any{item})}
			got, err := codexbinding.Codex.Guidance(nil).Threads(input, replacements)
			if wantError {
				if err == nil {
					t.Fatal("accepted conflicting admitted content")
				}
				return
			}
			if err != nil || got.InputChanged || got.InstructionsChanged || !bytes.Equal(got.Input, input.Input) {
				t.Fatalf("changed unrelated input: %+v, %v", got, err)
			}
		})
	}
}
