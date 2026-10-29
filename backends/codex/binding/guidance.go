package binding

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
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

// EnvironmentContext carries admitted runtime identity and its authoritative
// refresh revision. It conveys facts acquisition scope, never authorization.
type EnvironmentContext struct {
	Session, Thread, Workspace string
	Revision                   uint64
}

// FrontendGuidance separates executable capabilities from task workflow policy.
// All projections belong to the same authenticated invocation catalog.
type FrontendGuidance struct {
	Descriptions string `json:"descriptions"`
	Workflow     string `json:"workflow"`
	Shell        string `json:"shell,omitempty"` // Native-shell descriptions and shared workflow, without Codex execution framing.
}

// Guidance registers the distinct existing request phases. Callbacks are
// invocation-immutable; the application owns request copies and catalog indexing.
type Guidance struct {
	Environment       func(context.Context, InstructionInput, EnvironmentContext) (InstructionUpdate, error)
	Threads           func(InstructionInput, []guidance.Replacement) (InstructionUpdate, error)
	BeforePreparation func(InstructionInput) (InstructionUpdate, error)
	ForExecution      func(InstructionInput) (InstructionUpdate, error)
	AfterAttachments  func(InstructionInput, bool) (InstructionUpdate, error)
	Frontends         func([]command.Spec) (FrontendGuidance, error)
	ToolDescription   func(description, guide string, remove bool) (string, error)
	BaseInstructions  func(instructions json.RawMessage, workflow string, remove bool) (json.RawMessage, error)
}

func (r *Registry) Guidance(guidance Guidance) error {
	if guidance.BeforePreparation == nil || guidance.ForExecution == nil || guidance.AfterAttachments == nil || guidance.Frontends == nil || guidance.ToolDescription == nil || guidance.BaseInstructions == nil {
		return errors.New("guidance requires all request phases and frontend operations")
	}
	if r.guidance != nil {
		return errors.New("duplicate guidance registration")
	}
	r.guidance = &guidance
	return nil
}

func (r *Registry) RegisteredGuidance() *Guidance { return r.guidance }
