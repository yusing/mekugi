package sudoapproval

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sudoapproval/codexbinding"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
)

var Module = module.Descriptor[Deps, sudo.Service, codexbinding.Bindings, module.NoViews]{
	ID: "sudoapproval", Summary: "Own sudo approval policy and exact session grants", Lifetime: module.Invocation,
	New: newService, Bindings: codexbinding.Codex,
}
