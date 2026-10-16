package conversation

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type Deps struct{}

func newService(Deps, module.Slots) (composer.Service, error) { return composer.Service{}, nil }
