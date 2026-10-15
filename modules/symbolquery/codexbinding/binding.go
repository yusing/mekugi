package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/symbolquery/query"
)

type Bindings = binding.Bindings[query.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Register: Bind,
}

func Bind(registrar binding.Registrar, _ query.Service) error {
	return registrar.Frontend(query.Command.Spec)
}
