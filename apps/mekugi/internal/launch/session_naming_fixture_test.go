package launch

import (
	"context"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/sessionnaming"
)

const sessionTitleModel = "gpt-6-luna"

var testNaming = sync.OnceValue(func() *binding.SessionNaming {
	service, err := sessionnaming.Module.New(sessionnaming.Deps{}, module.Slots{})
	if err != nil {
		panic(err)
	}
	registry := new(binding.Registry)
	if err := sessionnaming.Module.Bindings.Register(registry, service, nil); err != nil {
		panic(err)
	}
	return registry.RegisteredNaming()
})

func newTestSessionNaming(ctx context.Context, provider inferencegateway.Forwarder) *sessionNaming {
	return newSessionNaming(ctx, provider, testNaming())
}

func runNamingOperation(ctx context.Context, g *sessionNaming, thread, prompt string) (string, error) {
	entry := testNaming().NewThread("", false)
	entry.ObserveInput(responses.MustMarshalJSON(prompt))
	complete := requestexecution.NamingCompletion(g.provider, g.usage, thread, sessionTitleAuthHeaders())
	return entry.Admit()(ctx, complete)
}
