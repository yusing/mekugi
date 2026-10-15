package fileread

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/fileread/codexbinding"
	"github.com/yusing/mekugi/modules/fileread/read"
)

var Module = module.Descriptor[Deps, read.Service, codexbinding.Bindings]{
	ID: "fileread", Summary: "Read bounded logical source rows", Lifetime: module.Stateless,
	Contributes: module.Slots{Commands: []command.Contribution{read.Command}},
	New:         newService, Bindings: codexbinding.Codex,
}
