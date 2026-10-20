package outputdeduplication

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/outputdeduplication/internal/projection"
)

type Deps struct{}

func newService(Deps, module.Slots) (projection.Service, error) { return projection.Service{}, nil }
