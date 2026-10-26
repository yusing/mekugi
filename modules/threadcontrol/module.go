package threadcontrol

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type Deps struct {
	History *history.Service
	Shared  *control.Service
}

var Module = module.Descriptor[Deps, *codexbinding.Driver, codexbinding.Bindings, module.NoViews]{
	ID: "threadcontrol", Summary: "Coordinate independent Codex threads and retained targeted waits",
	Requires: []module.ID{"sessionhistory", "agentguidance"}, Lifetime: module.Invocation,
	Contributes: module.Contributions[*codexbinding.Driver]{RecordKinds: control.RecordKinds()},
	Bindings:    codexbinding.Codex,
	Start:       func(ctx context.Context, driver *codexbinding.Driver) error { return driver.Start(ctx) },
	Stop:        func(ctx context.Context, driver *codexbinding.Driver) error { return driver.Stop(ctx) },
	New: func(deps Deps, _ module.Slots) (*codexbinding.Driver, error) {
		if deps.History == nil {
			return nil, errors.New("thread control requires session history")
		}
		owner := deps.Shared
		if owner == nil {
			owner = control.New()
		}
		return codexbinding.NewDriver(owner)
	},
}
