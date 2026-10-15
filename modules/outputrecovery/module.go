package outputrecovery

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/outputrecovery/codexbinding"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct{ History *history.Service }

var Module = module.Descriptor[Deps, *recovery.Service, codexbinding.Bindings]{
	ID: "outputrecovery", Summary: "Publish and page retained output", Requires: []module.ID{"sessionhistory"}, Lifetime: module.Stateless,
	HostsReferences: true,
	Contributes:     module.Contributions[*recovery.Service]{Guidance: []guidance.Section{{Command: recovery.Command.Name, Text: commandGuidance}}, Commands: []command.Definition[*recovery.Service]{recovery.Command}, RecordKinds: recovery.RecordKinds()},
	New:             newService, Bindings: codexbinding.Codex,
}

func newService(deps Deps, slots module.Slots) (*recovery.Service, error) {
	return recovery.New(deps.History, slots.References)
}
