package sudoapproval

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
)

type Deps struct{}

func newService(Deps, module.Slots) (sudo.Service, error) { return sudo.Service{}, nil }
