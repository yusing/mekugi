package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

type Bindings = binding.Bindings[*policy.Service]

var Codex = Bindings{
	Frontends: true,
	Guidance: func(service *policy.Service) binding.Guidance {
		return binding.Guidance{
			ContextUsage: func(input binding.InstructionInput, admitted binding.ContextUsage) (binding.InstructionUpdate, error) {
				return projectContextUsage(input, admitted, &service.ContextNotices)
			},
			InitialContext:    environmentPrefix,
			Threads:           projectThreadInstructions,
			BeforePreparation: beforePreparation,
			AfterAttachments:  afterAttachments,
			Frontends: func(commands []command.Spec) (binding.FrontendGuidance, error) {
				return frontendGuide(commands, service.Sections, service.ChildReplySummary)
			},
			ToolDescription:  toolDescription,
			BaseInstructions: baseInstructions,
		}
	},
}
