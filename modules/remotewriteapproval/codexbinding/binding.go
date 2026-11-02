package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/remotewriteapproval/remote"
)

type Bindings = binding.Bindings[remote.Service]

var Codex = Bindings{
	ShellApproval: func(remote.Service) binding.ShellApproval {
		return binding.ShellApproval{Kind: binding.RemoteWrite}
	},
}
