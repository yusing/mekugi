package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/sessionnaming/internal/naming"
)

type Bindings = binding.Bindings[naming.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.ThreadNaming, Required: true}},
	Register: Bind,
}

func Bind(registrar binding.Registrar, service naming.Service) error {
	return registrar.SessionNaming(binding.SessionNaming{
		NewThread: func(name string, hasTurns bool) binding.ThreadNaming {
			return thread{service.NewThread(name, hasTurns)}
		},
	})
}

type thread struct{ *naming.State }

func (t thread) ObserveInput(input []byte) {
	if t.NeedsPrompt() {
		t.RetainPrompt(binding.SubmittedUserText(input, "/root", true))
	}
}
