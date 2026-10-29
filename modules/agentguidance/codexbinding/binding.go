package codexbinding

import (
	"context"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

type Bindings = binding.Bindings[*policy.Service]

var Codex = Bindings{
	Requires:  []capability.Requirement{{Mechanism: capability.GuidanceProjection, Required: true}, {Mechanism: capability.ExecutableFrontend, Required: true}},
	Frontends: true,
	Guidance: func(service *policy.Service) binding.Guidance {
		return binding.Guidance{
			Environment: func(ctx context.Context, input binding.InstructionInput, admitted binding.EnvironmentContext) (binding.InstructionUpdate, error) {
				return projectEnvironment(ctx, input, admitted, &service.Environment)
			},
			Threads:           projectThreadInstructions,
			BeforePreparation: beforePreparation,
			ForExecution: func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
				return forExecution(input, service.Conflicts)
			},
			AfterAttachments: afterAttachments,
			Frontends: func(commands []command.Spec) (binding.FrontendGuidance, error) {
				return frontendGuide(commands, service.Sections, service.ChildReplySummary)
			},
			ToolDescription:  toolDescription,
			BaseInstructions: baseInstructions,
		}
	},
}
