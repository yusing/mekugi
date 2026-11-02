package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
)

type Bindings = binding.Bindings[sudo.Service]

var Codex = Bindings{
	ShellApproval: func(sudo.Service) binding.ShellApproval {
		return binding.ShellApproval{Kind: binding.Sudo}
	},
}
