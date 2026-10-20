package sourceoutline

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
)

type Deps struct{}

func newService(Deps, module.Slots) (outline.Service, error) { return outline.Service{}, nil }
