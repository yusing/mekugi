package symbolquery

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/symbolquery/codexbinding"
	"github.com/yusing/mekugi/modules/symbolquery/query"
)

var Module = module.Descriptor[Deps, query.Service, codexbinding.Bindings]{
	ID: "symbolquery", Summary: "Resolve semantic definitions and references", Lifetime: module.HostOperation,
	Contributes: module.Contributions[query.Service]{Commands: []command.Definition[query.Service]{query.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
