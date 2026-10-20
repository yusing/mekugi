package agentguidance

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance/codexbinding"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

var Module = module.Descriptor[Deps, *policy.Service, codexbinding.Bindings, module.NoViews]{
	ID: "agentguidance", Summary: "Assemble agent guidance without changing caller authority", Lifetime: module.Stateless,
	HostsGuidance: true, New: newService, Bindings: codexbinding.Codex,
}
