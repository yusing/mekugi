package launch

import (
	"sync"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/remotewriteapproval"
	"github.com/yusing/mekugi/modules/sudoapproval"
)

var testGuards = sync.OnceValue(func() []binding.ShellApproval {
	registry := new(binding.Registry)
	remote, err := remotewriteapproval.Module.New(remotewriteapproval.Deps{}, module.Slots{})
	if err != nil {
		panic(err)
	}
	if err := remotewriteapproval.Module.Bindings.Register(registry, remote); err != nil {
		panic(err)
	}
	sudo, err := sudoapproval.Module.New(sudoapproval.Deps{}, module.Slots{})
	if err != nil {
		panic(err)
	}
	if err := sudoapproval.Module.Bindings.Register(registry, sudo); err != nil {
		panic(err)
	}
	return registry.RegisteredGuards()
})
