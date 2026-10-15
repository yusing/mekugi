package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
)

type Bindings = binding.Bindings[run.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Register: Bind,
}

func Bind(registrar binding.Registrar, _ run.Service) error {
	return registrar.Frontend(run.Command.Spec)
}
