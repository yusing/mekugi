package changehistory

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/codexbinding"
	"github.com/yusing/mekugi/modules/changehistory/internal/retained"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct{ History *history.Service }

var Module = module.Descriptor[Deps, changes.Service, codexbinding.Bindings]{ID: "changehistory", Summary: "Retain, review, apply, and revert filesystem change evidence", Requires: []module.ID{"sessionhistory"}, Lifetime: module.Stateless, Contributes: module.Contributions[changes.Service]{Guidance: []guidance.Section{{Command: changes.Command.Name, Text: commandGuidance}}, Commands: []command.Definition[changes.Service]{changes.Command}, RecordKinds: retained.RecordKinds(), References: retained.ChangeReferences()}, New: newService, Bindings: codexbinding.Codex}

func newService(deps Deps, _ module.Slots) (changes.Service, error) {
	return retained.New(deps.History)
}
