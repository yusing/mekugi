package boundedcommand

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/boundedcommand/codexbinding"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
)

var Module = module.Descriptor[Deps, run.Service, codexbinding.Bindings, module.NoViews]{
	ID: "boundedcommand", Summary: "Bound one foreground command's output", Lifetime: module.HostOperation,
	Contributes: module.Contributions[run.Service]{Guidance: []guidance.Section{{Command: run.Command.Name, Text: commandGuidance}}, Commands: []command.Definition[run.Service]{run.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
