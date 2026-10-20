package boundedcommand

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
)

type Deps struct{}

func newService(Deps, module.Slots) (run.Service, error) { return run.Service{}, nil }
