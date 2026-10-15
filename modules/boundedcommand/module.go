package boundedcommand

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/boundedcommand/codexbinding"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
)

var Module = module.Descriptor[Deps, run.Service, codexbinding.Bindings]{
	ID: "boundedcommand", Summary: "Bound one foreground command's output", Lifetime: module.HostOperation,
	Contributes: module.Contributions[run.Service]{Commands: []command.Definition[run.Service]{run.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
