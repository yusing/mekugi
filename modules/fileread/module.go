package fileread

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/fileread/codexbinding"
	"github.com/yusing/mekugi/modules/fileread/read"
)

var Module = module.Descriptor[Deps, read.Service, codexbinding.Bindings, module.NoViews]{
	ID: "fileread", Summary: "Read bounded logical source rows", Lifetime: module.Stateless,
	Contributes: module.Contributions[read.Service]{Guidance: []guidance.Section{{Command: read.Command.Name, Text: commandGuidance}}, Commands: []command.Definition[read.Service]{read.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
