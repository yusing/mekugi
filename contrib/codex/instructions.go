package codexinstructions

import (
	_ "embed"
	"strings"
	"text/template"
)

//go:embed file-editing-instructions.md
var instructionSource string

//go:embed editing-workflow-default.md
var defaultWorkflow string

//go:embed editing-workflow-astra.md
var astraWorkflow string

var instructionTemplate = template.Must(template.New("model-instructions").Parse(instructionSource))
var instructions = renderInstructions(defaultWorkflow)
var astraInstructions = renderInstructions(astraWorkflow)

func renderInstructions(workflow string) string {
	var rendered strings.Builder
	if err := instructionTemplate.Execute(&rendered, struct{ EditingWorkflow string }{strings.TrimSuffix(workflow, "\n")}); err != nil {
		panic(err)
	}
	return rendered.String()
}

//go:embed mekugi-recovery.tmpl
var recoverySource string

var recoveryTemplate = template.Must(template.New("mekugi-recovery").Parse(recoverySource))

// InstructionsForModel selects the editing workflow per request, independently of transport.
// Unknown model IDs use the default workflow; Astra-prefixed variants share the Astra workflow.
func InstructionsForModel(model string, compactModelProtocol bool) string {
	selected := instructions
	if WorkflowForModel(model) == "astra" {
		selected = astraInstructions
	}
	if !compactModelProtocol {
		return nativeInstructions(selected)
	}
	return selected
}

// WorkflowForModel identifies the selected wording independently of the incoming prompt shape.
func WorkflowForModel(model string) string {
	if model == "gpt-6-astra" || strings.HasPrefix(model, "gpt-6-astra-") {
		return "astra"
	}
	return "default"
}

func nativeInstructions(instructions string) string {
	const (
		ctpHeading         = "## CTP/2 transport\n"
		fileEditingHeading = "## File editing\n"
	)
	start := strings.Index(instructions, ctpHeading)
	if start < 0 {
		panic("central model instructions omit the CTP heading")
	}
	remainder := instructions[start:]
	end := strings.Index(remainder, fileEditingHeading)
	if end < 0 {
		panic("central model instructions omit the file-editing heading after CTP")
	}
	return instructions[:start] + remainder[end:]
}

// RecoveryGuidance renders dynamic rejected-script guidance.
func RecoveryGuidance(references string) string {
	var rendered strings.Builder
	if err := recoveryTemplate.Execute(&rendered, struct{ References string }{references}); err != nil {
		panic(err)
	}
	return rendered.String()
}
