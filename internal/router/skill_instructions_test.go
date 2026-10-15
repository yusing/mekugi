package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRewriteRequestSelectedSkillInstructions(t *testing.T) {
	selected := "<skill>\n<name>demo</name>\n<path>/tmp/demo/SKILL.md</path>\nbody\n</skill>"
	want := `<skill name="demo"/>`
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{
			map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": selected}, map[string]string{"type": "input_image", "image_url": selected}}},
			map[string]string{"role": "developer", "content": selected},
			map[string]string{"role": "user", "content": "quote " + selected},
		}),
	}}
	if err := request.projectInstructions(func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
		return testGuidance().AfterAttachments(input, true)
	}); err != nil {
		t.Fatal(err)
	}
	wantInput := responses.MustMarshalJSON([]any{
		map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": want}, map[string]string{"type": "input_image", "image_url": selected}}},
		map[string]string{"role": "developer", "content": selected},
		map[string]string{"role": "user", "content": "quote " + selected},
	})
	if !sameJSONValue(request.fields["input"], wantInput) {
		t.Fatalf("input = %s, want %s", request.fields["input"], wantInput)
	}
	before := string(request.fields["input"])
	if err := request.projectInstructions(func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
		return testGuidance().AfterAttachments(input, true)
	}); err != nil || string(request.fields["input"]) != before {
		t.Fatalf("repeat changed input: %v", err)
	}
}

func TestRewriteRequestSelectedSkillInstructionsDisabled(t *testing.T) {
	input := responses.MustMarshalJSON([]any{map[string]string{
		"role": "user", "content": "<skill>\n<name>demo</name>\n<path>/tmp/demo/SKILL.md</path>\nbody\n</skill>",
	}})
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": input}}
	if err := request.projectInstructions(func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
		return testGuidance().AfterAttachments(input, false)
	}); err != nil || !sameJSONValue(request.fields["input"], input) {
		t.Fatalf("disabled rewrite changed input: %v, %s", err, request.fields["input"])
	}
}

func TestSkillsManagerInExtraPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	directory := t.TempDir()
	path := filepath.Join(directory, "skills-mgr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !skillsManagerInPath(directory) {
		t.Fatal("skills-mgr executable in frontend directory was not found")
	}
	if skillsManagerInPath(t.TempDir()) {
		t.Fatal("skills-mgr reported available outside PATH")
	}
}

func TestSkillsManagerInRelativePath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "skills-mgr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(workingDirectory, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", relative)
	if !skillsManagerInPath("") {
		t.Fatal("skills-mgr executable in relative PATH was not found")
	}
}
