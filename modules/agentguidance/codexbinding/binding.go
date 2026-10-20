package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

type Bindings = binding.Bindings[*policy.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.GuidanceProjection, Required: true}},
	Guidance: func(service *policy.Service) binding.Guidance {
		return binding.Guidance{
			BeforePreparation: beforePreparation,
			ForExecution: func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
				return forExecution(input, service.Conflicts)
			},
			AfterAttachments: afterAttachments,
			Frontends: func(commands []command.Spec) (binding.FrontendGuidance, error) {
				return frontendGuide(commands, service.Sections)
			},
			ToolDescription:  toolDescription,
			BaseInstructions: baseInstructions,
		}
	},
}
