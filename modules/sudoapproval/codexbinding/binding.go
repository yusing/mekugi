package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
)

type Bindings = binding.Bindings[sudo.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ShellApproval, Required: true}},
	ShellApproval: func(service sudo.Service) binding.ShellApproval {
		return binding.ShellApproval{Kind: binding.Sudo, NewSession: func() binding.CommandGrants { return service.NewSession() }}
	},
}
