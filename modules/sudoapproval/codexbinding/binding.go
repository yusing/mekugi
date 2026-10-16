package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
)

type Bindings = binding.Bindings[sudo.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ShellApproval, Required: true}},
	Register: func(r binding.Registrar, service sudo.Service) error {
		return r.ShellApproval(binding.ShellApproval{Kind: binding.Sudo, NewSession: func() binding.CommandGrants { return service.NewSession() }})
	},
}
