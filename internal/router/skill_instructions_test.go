package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRewriteSelectedSkillInstructions(t *testing.T) {
	selected := "<skill>\n<name>golang-best-practices</name>\n<path>/home/user/.agents/skills/golang-best-practices/SKILL.md</path>\n---\nname: golang-best-practices\n---\n\n</skill>"
	want := `<skill name="golang-best-practices"/>`
	for _, test := range []struct{ name, input, want string }{
		{"selected skill", selected, want},
		{"surrounding whitespace", " \n" + selected + "\n ", " \n" + want + "\n "},
		{"resource access collapsed", "<skill>\n<name>remote</name>\n<path>skill://remote/SKILL.md</path>\n<resource_access>{}</resource_access>\nbody\n</skill>", `<skill name="remote"/>`},
		{"name escaped", "<skill>\n<name>a & \"b\"</name>\n<path>/tmp/SKILL.md</path>\nbody\n</skill>", `<skill name="a &amp; &#34;b&#34;"/>`},
		{"ordinary user text", "Please quote " + selected, "Please quote " + selected},
		{"missing name", "<skill>\n<path>/tmp/SKILL.md</path>\nbody\n</skill>", "<skill>\n<path>/tmp/SKILL.md</path>\nbody\n</skill>"},
		{"path in body", "<skill>\n<name>demo</name>\nbody <path>keep</path>\n</skill>", "<skill>\n<name>demo</name>\nbody <path>keep</path>\n</skill>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rewriteSelectedSkillInstructions(test.input); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestRewriteRequestSelectedSkillInstructions(t *testing.T) {
	selected := "<skill>\n<name>demo</name>\n<path>/tmp/demo/SKILL.md</path>\nbody\n</skill>"
	want := `<skill name="demo"/>`
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{
			map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": selected}, map[string]string{"type": "input_image", "image_url": selected}}},
			map[string]string{"role": "developer", "content": selected},
			map[string]string{"role": "user", "content": "quote " + selected},
		}),
	}}
	if err := rewriteRequestSelectedSkillInstructions(&request, true); err != nil {
		t.Fatal(err)
	}
	wantInput := mustMarshalJSON([]any{
		map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": want}, map[string]string{"type": "input_image", "image_url": selected}}},
		map[string]string{"role": "developer", "content": selected},
		map[string]string{"role": "user", "content": "quote " + selected},
	})
	if !sameJSONValue(request.fields["input"], wantInput) {
		t.Fatalf("input = %s, want %s", request.fields["input"], wantInput)
	}
	before := string(request.fields["input"])
	if err := rewriteRequestSelectedSkillInstructions(&request, true); err != nil || string(request.fields["input"]) != before {
		t.Fatalf("repeat changed input: %v", err)
	}
}

func TestRewriteRequestSelectedSkillInstructionsDisabled(t *testing.T) {
	input := mustMarshalJSON([]any{map[string]string{
		"role": "user", "content": "<skill>\n<name>demo</name>\n<path>/tmp/demo/SKILL.md</path>\nbody\n</skill>",
	}})
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": input}}
	if err := rewriteRequestSelectedSkillInstructions(&request, false); err != nil || !sameJSONValue(request.fields["input"], input) {
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
