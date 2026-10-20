package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/symbolquery/query"
)

type Bindings = binding.Bindings[query.Service]

var Codex = Bindings{
	Requires:  []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Frontends: []command.Spec{query.Command.Spec},
}
