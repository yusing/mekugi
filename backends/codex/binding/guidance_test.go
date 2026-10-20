package binding

import (
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestGuidanceRegistration(t *testing.T) {
	var registry Registry
	if registry.RegisteredGuidance() != nil {
		t.Fatal("unregistered guidance is present")
	}
	if err := registry.Guidance(Guidance{}); err == nil {
		t.Fatal("incomplete guidance accepted")
	}
	project := func(input InstructionInput) (InstructionUpdate, error) {
		return InstructionUpdate{InstructionInput: input}, nil
	}
	guidance := Guidance{
		BeforePreparation: project, ForExecution: project,
		AfterAttachments: func(input InstructionInput, _ bool) (InstructionUpdate, error) { return project(input) },
		Frontends:        func([]command.Spec) (string, error) { return "", nil },
		ToolDescription:  func(description, _ string, _ bool) (string, error) { return description, nil },
	}
	if err := registry.Guidance(guidance); err != nil {
		t.Fatal(err)
	}
	if err := registry.Guidance(guidance); err == nil {
		t.Fatal("duplicate guidance accepted")
	}
}
