package sessionnaming

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionnaming/internal/naming"
)

type Deps struct{}

func newService(Deps, module.Slots) (naming.Service, error) { return naming.Service{}, nil }
