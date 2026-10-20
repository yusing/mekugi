package executionobservation

import (
	"sync"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/outputrecovery"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/modules/symbolquery"
)

const (
	instructionOmitStart  = "<!-- mekugi:omit -->"
	instructionOmitEnd    = "<!-- /mekugi:omit -->"
	frontendGuidanceStart = "<!-- mekugi-frontends:start -->"
	frontendGuidanceEnd   = "<!-- mekugi-frontends:end -->"
)

var testGuidance = sync.OnceValue(func() *binding.Guidance {
	var sections []guidance.Section
	for _, group := range [][]guidance.Section{
		fileread.Module.Contributes.Guidance, sourceoutline.Module.Contributes.Guidance,
		symbolquery.Module.Contributes.Guidance, outputrecovery.Module.Contributes.Guidance,
		changehistory.Module.Contributes.Guidance, boundedcommand.Module.Contributes.Guidance,
	} {
		sections = append(sections, group...)
	}
	service, err := agentguidance.Module.New(agentguidance.Deps{}, module.Slots{Guidance: sections})
	if err != nil {
		panic(err)
	}
	registry := new(binding.Registry)
	if err := agentguidance.Module.Bindings.Register(registry, service); err != nil {
		panic(err)
	}
	return registry.RegisteredGuidance()
})
