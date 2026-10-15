package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type Bindings = binding.Bindings[changes.Service]

var Codex = Bindings{Requires: []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}}, Register: Bind}

func Bind(registrar binding.Registrar, _ changes.Service) error {
	return registrar.Frontend(changes.Command.Spec)
}
