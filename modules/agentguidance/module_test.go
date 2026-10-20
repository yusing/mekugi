package agentguidance_test

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func registeredGuidance(t *testing.T, sections ...guidance.Section) *binding.Guidance {
	t.Helper()
	script := &moduletest.Scripted{}
	moduletest.New(t, agentguidance.Module, agentguidance.Deps{}, module.Slots{Guidance: sections}, script)
	if script.RegisteredGuidance() == nil {
		t.Fatal("module did not register guidance")
	}
	return script.RegisteredGuidance()
}

func encoded(t *testing.T, value any) jsonv1.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodedText(t *testing.T, data jsonv1.RawMessage) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		t.Fatal(err)
	}
	return text
}

func TestModuleGuidanceWithoutContributions(t *testing.T) {
	g := registeredGuidance(t)
	if !agentguidance.Module.HostsGuidance {
		t.Fatal("descriptor must host the guidance slot")
	}
	guide, err := g.Frontends(nil)
	if err != nil || guide != "" {
		t.Fatalf("empty catalog = %q, %v", guide, err)
	}
	const conflict = "- You share updates in the `commentary` channel."
	input := binding.InstructionInput{
		Instructions: encoded(t, "Keep authority.\n<!-- mekugi:omit -->private<!-- /mekugi:omit -->\n"+conflict+"\nKeep delegation."),
		Input:        jsonv1.RawMessage(`[{"role":"user","content":"Keep user text."}]`),
	}
	early, err := g.BeforePreparation(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodedText(t, early.Instructions); got != "Keep authority.\n\n"+conflict+"\nKeep delegation." || !early.InstructionsChanged || early.InputChanged || !bytes.Equal(early.Input, input.Input) {
		t.Fatalf("early projection = %+v", early)
	}
	execution, err := g.ForExecution(early.InstructionInput)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodedText(t, execution.Instructions); got != "Keep authority.\n\n\nKeep delegation." || !execution.InstructionsChanged || execution.InputChanged || !bytes.Equal(execution.Input, input.Input) {
		t.Fatalf("execution projection = %+v", execution)
	}
	again, err := g.ForExecution(execution.InstructionInput)
	if err != nil || again.InstructionsChanged || again.InputChanged || !bytes.Equal(again.Instructions, execution.Instructions) {
		t.Fatalf("repeated projection = %+v, %v", again, err)
	}
}

func TestModuleGuidanceSelectedCatalog(t *testing.T) {
	g := registeredGuidance(t,
		guidance.Section{Command: "reader", Text: "Selected workflow."},
		guidance.Section{Command: "absent", Text: "Absent workflow."},
	)
	commands := []command.Spec{
		{Name: "reader", Description: "Read exact source.\nUsage: reader PATH"},
		{Name: "other", Description: "Another exact description."},
	}
	guide, err := g.Frontends(commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range commands {
		if strings.Count(guide, spec.Description) != 1 {
			t.Fatalf("description %q was not included exactly once: %s", spec.Description, guide)
		}
	}
	if !strings.Contains(guide, "<tool name=\"reader\">\n"+commands[0].Description+"\nSelected workflow.\n</tool>") || strings.Contains(guide, "Absent workflow.") || strings.Contains(guide, `<tool name="absent">`) {
		t.Fatalf("selected catalog = %s", guide)
	}
	projected, err := g.ToolDescription("Stock execution contract.", guide, false)
	if err != nil || projected != "Stock execution contract.\n\n"+guide {
		t.Fatalf("description projection = %q, %v", projected, err)
	}
	commands[0].Description = "Updated authoritative description."
	refreshed, err := g.Frontends(commands)
	if err != nil {
		t.Fatal(err)
	}
	projected, err = g.ToolDescription(projected, refreshed, false)
	if err != nil || projected != "Stock execution contract.\n\n"+refreshed || strings.Contains(projected, "Read exact source.") {
		t.Fatalf("description refresh = %q, %v", projected, err)
	}
}

func TestModuleGuidanceRejectsInvalidSupplements(t *testing.T) {
	for _, test := range []struct {
		name     string
		sections []guidance.Section
	}{
		{"missing command", []guidance.Section{{Text: "Workflow."}}},
		{"blank text", []guidance.Section{{Command: "reader", Text: " \n\t"}}},
		{"duplicate", []guidance.Section{{Command: "reader", Text: "First."}, {Command: "reader", Text: "Second."}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := agentguidance.Module.New(agentguidance.Deps{}, module.Slots{Guidance: test.sections}); err == nil {
				t.Fatal("invalid guidance contribution accepted")
			}
		})
	}
}

func TestModuleGuidanceRejectsFraming(t *testing.T) {
	for _, framing := range []string{"<!-- mekugi-frontends:start -->", "<!-- mekugi-frontends:end -->", `</tool>`, `<mekugi-frontends>`} {
		t.Run(framing, func(t *testing.T) {
			g := registeredGuidance(t, guidance.Section{Command: "reader", Text: framing})
			if _, err := g.Frontends([]command.Spec{{Name: "reader", Description: "Read."}}); err == nil {
				t.Fatal("supplement framing accepted")
			}
			g = registeredGuidance(t)
			if _, err := g.Frontends([]command.Spec{{Name: "reader", Description: framing}}); err == nil {
				t.Fatal("description framing accepted")
			}
		})
	}
	g := registeredGuidance(t)
	const start, end = "<!-- mekugi-frontends:start -->", "<!-- mekugi-frontends:end -->"
	for _, description := range []string{start, end, end + start, start + start + end} {
		if _, err := g.ToolDescription(description, "replacement", false); err == nil {
			t.Errorf("malformed description accepted: %q", description)
		}
	}
}

func TestModuleGuidanceLateSelectedSkillFlag(t *testing.T) {
	g := registeredGuidance(t)
	const selected = "<skill>\n<name>example</name>\n<path>/skills/example/SKILL.md</path>\nSelected instructions.\n</skill>"
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	messages := []message{
		{Role: "user", Content: selected},
		{Role: "developer", Content: selected},
		{Role: "user", Content: "Please quote " + selected},
	}
	input := binding.InstructionInput{Instructions: encoded(t, selected), Input: encoded(t, messages)}
	off, err := g.AfterAttachments(input, false)
	if err != nil || off.InstructionsChanged || off.InputChanged || !bytes.Equal(off.Input, input.Input) || !bytes.Equal(off.Instructions, input.Instructions) {
		t.Fatalf("disabled skill projection = %+v, %v", off, err)
	}
	on, err := g.AfterAttachments(input, true)
	if err != nil {
		t.Fatal(err)
	}
	if !on.InputChanged || on.InstructionsChanged || !bytes.Equal(on.Instructions, input.Instructions) {
		t.Fatalf("enabled skill projection = %+v", on)
	}
	var got []message
	if err := json.Unmarshal(on.Input, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(got), len(messages))
	}
	for i, want := range messages {
		if i == 0 {
			want.Content = `<skill name="example"/>`
		}
		if got[i] != want {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want)
		}
	}
	again, err := g.AfterAttachments(on.InstructionInput, true)
	if err != nil || again.InputChanged || again.InstructionsChanged || !bytes.Equal(again.Input, on.Input) {
		t.Fatalf("repeated skill projection = %+v, %v", again, err)
	}
}
