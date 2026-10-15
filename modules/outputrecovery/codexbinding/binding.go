package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

type Bindings = binding.Bindings[*recovery.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Register: Bind,
}

func Bind(registrar binding.Registrar, _ *recovery.Service) error {
	return registrar.Frontend(recovery.Command.Spec)
}
