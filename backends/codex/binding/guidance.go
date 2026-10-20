package binding

import (
	"encoding/json"
	"errors"

	"github.com/yusing/mekugi/contracts/command"
)

type InstructionInput struct {
	Instructions json.RawMessage
	Input        json.RawMessage
}

type InstructionUpdate struct {
	InstructionInput
	InstructionsChanged bool
	InputChanged        bool
}

// Guidance registers the distinct existing request phases. Callbacks are
// invocation-immutable; the application owns request copies and catalog indexing.
type Guidance struct {
	BeforePreparation func(InstructionInput) (InstructionUpdate, error)
	ForExecution      func(InstructionInput) (InstructionUpdate, error)
	AfterAttachments  func(InstructionInput, bool) (InstructionUpdate, error)
	Frontends         func([]command.Spec) (string, error)
	ToolDescription   func(description, guide string, remove bool) (string, error)
}

func (r *Registry) Guidance(guidance Guidance) error {
	if guidance.BeforePreparation == nil || guidance.ForExecution == nil || guidance.AfterAttachments == nil || guidance.Frontends == nil || guidance.ToolDescription == nil {
		return errors.New("guidance requires all request phases and frontend operations")
	}
	if r.guidance != nil {
		return errors.New("duplicate guidance registration")
	}
	r.guidance = &guidance
	return nil
}

func (r *Registry) RegisteredGuidance() *Guidance { return r.guidance }
