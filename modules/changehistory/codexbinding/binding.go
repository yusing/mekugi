package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/changehistory/capture"
)

type Bindings = binding.Bindings[*capture.Service]

var Codex = Bindings{
	Requires:  []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}},
	Frontends: true,
}
