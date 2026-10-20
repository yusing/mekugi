// Source: apps/mekugi/internal/router/request_guidance.go:1:20@b9679c93.
package requestexecution

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func ProjectInstructions(request *codexresponses.Request, project func(binding.InstructionInput) (binding.InstructionUpdate, error)) error {
	update, err := project(binding.InstructionInput{Instructions: request.Fields["instructions"], Input: request.Fields["input"]})
	if err != nil {
		return err
	}
	if update.InstructionsChanged {
		request.Fields["instructions"] = update.Instructions
	}
	if update.InputChanged {
		request.Fields["input"] = update.Input
	}
	return nil
}
