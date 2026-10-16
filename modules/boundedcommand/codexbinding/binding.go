package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
)

type Bindings = binding.Bindings[run.Service]

var Codex = Bindings{
	Requires:  []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Frontends: []command.Spec{run.Command.Spec},
}
