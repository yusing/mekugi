package agentguidance

import (
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/agentguidance/codexbinding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

type Deps = compact.CaptureService

var Module = module.Descriptor[Deps, *compact.CaptureService, codexbinding.Bindings, module.NoViews]{
	ID: "agentguidance", Summary: "Capture admitted context sources and retain historical summaries", Lifetime: module.Stateless,
	Requires: []module.ID{"sessionhistory", "changehistory"},
	Contributes: module.Contributions[*compact.CaptureService]{
		RecordKinds: []recordkind.Spec{compact.Kind},
		Commands: []command.Definition[*compact.CaptureService]{
			{Name: "mreload", Run: (*compact.CaptureService).Capture},
		},
	},
	New:      func(deps Deps, _ module.Slots) (*compact.CaptureService, error) { return &deps, nil },
	Bindings: codexbinding.Codex,
}
