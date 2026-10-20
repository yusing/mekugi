package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
)

type Bindings = binding.Bindings[outline.Service]

var Codex = Bindings{
	Requires:  []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Frontends: []command.Spec{outline.Command.Spec},
}
