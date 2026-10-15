package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/fileread/read"
)

type Bindings = binding.Bindings[read.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Register: Bind,
}

func Bind(registrar binding.Registrar, _ read.Service) error {
	return registrar.Frontend(read.Command.Spec)
}
