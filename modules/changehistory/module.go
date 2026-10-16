package changehistory

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/codexbinding"
	"github.com/yusing/mekugi/modules/changehistory/internal/retained"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct {
	History *history.Service
	// Directory selects live capture storage; empty enables retained operations only.
	Directory string
}

var Module = module.Descriptor[Deps, *capture.Service, codexbinding.Bindings]{
	ID: "changehistory", Summary: "Capture, retain, review, apply, and revert filesystem change evidence",
	Requires: []module.ID{"sessionhistory"}, Lifetime: module.Invocation,
	Resources: []string{"private workspace checkpoints and bounded warm-up tasks"},
	Contributes: module.Contributions[*capture.Service]{
		Guidance:    []guidance.Section{{Command: changes.Command.Name, Text: commandGuidance}},
		Commands:    []command.Definition[*capture.Service]{{Spec: changes.Command.Spec, Run: (*capture.Service).Execute}},
		RecordKinds: retained.RecordKinds(), References: retained.ChangeReferences(),
	},
	New: newService, Bindings: codexbinding.Codex,
	Start: startService, Stop: stopService,
}
