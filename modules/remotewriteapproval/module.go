package remotewriteapproval

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/remotewriteapproval/codexbinding"
	"github.com/yusing/mekugi/modules/remotewriteapproval/remote"
)

var Module = module.Descriptor[Deps, remote.Service, codexbinding.Bindings]{
	ID: "remotewriteapproval", Summary: "Own remote-write approval policy and exact session grants", Lifetime: module.Invocation,
	New: newService, Bindings: codexbinding.Codex,
}
