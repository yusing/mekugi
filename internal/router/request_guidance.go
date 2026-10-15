package router

import "github.com/yusing/mekugi/backends/codex/binding"

func (request *parsedResponsesRequest) projectInstructions(project func(binding.InstructionInput) (binding.InstructionUpdate, error)) error {
	update, err := project(binding.InstructionInput{Instructions: request.fields["instructions"], Input: request.fields["input"]})
	if err != nil {
		return err
	}
	if update.InstructionsChanged {
		request.fields["instructions"] = update.Instructions
	}
	if update.InputChanged {
		request.setInput(update.Input)
	}
	return nil
}
