package agentguidance

import (
	"context"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/agentguidance/codexbinding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

var Module = module.Descriptor[Deps, *policy.Service, codexbinding.Bindings, module.NoViews]{
	ID: "agentguidance", Summary: "Assemble agent guidance without changing caller authority", Lifetime: module.Stateless,
	Requires: []module.ID{"sessionhistory"},
	Contributes: module.Contributions[*policy.Service]{RecordKinds: []recordkind.Spec{compact.Kind}, Commands: []command.Definition[*policy.Service]{
		{Name: "mreload", Description: "Capture selected compaction sources for complete context delivery. Usage: mreload SELECTION_JSON. Used by automatic reload; returns a retained snapshot reference after all sources finish.", Run: func(_ *policy.Service, ctx context.Context, request command.Request) (command.Output, error) {
			return compact.Capture(ctx, request)
		}},
	}},
	HostsGuidance: true, New: newService, Bindings: codexbinding.Codex,
}
