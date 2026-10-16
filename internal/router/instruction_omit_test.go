package router

import (
	"encoding/json"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestStripRequestInstructionOmissions(t *testing.T) {
	block := instructionOmitStart + "use rtk" + instructionOmitEnd
	wrapper := "# AGENTS.md instructions for /workspace\n<INSTRUCTIONS>keep" + block + "</INSTRUCTIONS>\n<environment_context>unchanged</environment_context>"
	for _, role := range []string{"system", "developer", "user", "assistant"} {
		for _, multipart := range []bool{false, true} {
			t.Run(role+map[bool]string{false: "/string", true: "/multipart"}[multipart], func(t *testing.T) {
				text := "keep" + block
				want := "keep"
				if role == "user" {
					text = wrapper
					want = strings.ReplaceAll(wrapper, block, "")
				}
				if role == "assistant" {
					want = text
				}
				var content any = text
				var expected any = want
				if multipart {
					image := map[string]string{"type": "input_image", "image_url": block}
					content = []any{map[string]string{"type": "input_text", "text": text}, image, map[string]string{"type": "text", "text": text}}
					expected = []any{map[string]string{"type": "input_text", "text": want}, image, map[string]string{"type": "text", "text": want}}
				}
				request := codexresponses.Request{Fields: map[string]json.RawMessage{
					"instructions": responses.MustMarshalJSON("base" + block),
					"input": responses.MustMarshalJSON([]any{
						map[string]any{"role": role, "content": content},
						map[string]string{"role": "user", "content": "Please explain " + block},
						map[string]string{"type": "function_call_output", "call_id": "call", "output": block},
					}),
				}}
				if err := projectRequestInstructions(&request, testGuidance().BeforePreparation); err != nil {
					t.Fatal(err)
				}
				if got := responses.StringField(request.Fields, "instructions"); got != "base" {
					t.Fatalf("instructions = %q", got)
				}
				wantInput := responses.MustMarshalJSON([]any{
					map[string]any{"role": role, "content": expected},
					map[string]string{"role": "user", "content": "Please explain " + block},
					map[string]string{"type": "function_call_output", "call_id": "call", "output": block},
				})
				if !responses.SameJSONValue(request.Fields["input"], wantInput) {
					t.Fatalf("input = %s, want %s", request.Fields["input"], wantInput)
				}
				before := string(request.Fields["input"])
				if err := projectRequestInstructions(&request, testGuidance().BeforePreparation); err != nil || string(request.Fields["input"]) != before {
					t.Fatalf("repeat changed input: %v", err)
				}
			})
		}
	}
}

func TestStripDirectorylessAgentInstructions(t *testing.T) {
	text := "# AGENTS.md instructions\n<INSTRUCTIONS>keep" + instructionOmitStart + "use rtk" + instructionOmitEnd + "</INSTRUCTIONS>"
	request := codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{map[string]string{"role": "user", "content": text}}),
	}}
	if err := projectRequestInstructions(&request, testGuidance().BeforePreparation); err != nil {
		t.Fatal(err)
	}
	want := responses.MustMarshalJSON([]any{map[string]string{"role": "user", "content": "# AGENTS.md instructions\n<INSTRUCTIONS>keep</INSTRUCTIONS>"}})
	if !responses.SameJSONValue(request.Fields["input"], want) {
		t.Fatalf("input = %s, want %s", request.Fields["input"], want)
	}
}
