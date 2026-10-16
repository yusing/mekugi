package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/remotewriteapproval/remote"
)

type Bindings = binding.Bindings[remote.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ShellApproval, Required: true}},
	Register: func(r binding.Registrar, service remote.Service) error {
		return r.ShellApproval(binding.ShellApproval{Kind: binding.RemoteWrite, NewSession: func() binding.CommandGrants { return service.NewSession() }})
	},
}
