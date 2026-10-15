package sourceoutline

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sourceoutline/codexbinding"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
)

var Module = module.Descriptor[Deps, outline.Service, codexbinding.Bindings]{
	ID: "sourceoutline", Summary: "Inspect structural declarations and source spans", Lifetime: module.Stateless,
	Contributes: module.Slots{Commands: []command.Contribution{outline.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
