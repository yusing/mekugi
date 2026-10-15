package sourceoutline

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sourceoutline/codexbinding"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
)

var Module = module.Descriptor[Deps, outline.Service, codexbinding.Bindings]{
	ID: "sourceoutline", Summary: "Inspect structural declarations and source spans", Lifetime: module.Stateless,
	Contributes: module.Contributions[outline.Service]{Guidance: []guidance.Section{{Command: outline.Command.Name, Text: commandGuidance}}, Commands: []command.Definition[outline.Service]{outline.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
