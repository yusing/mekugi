package conversation

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/conversation/presentation"
)

type Deps struct{}

func newService(Deps, module.Slots) (*presentation.Service, error) {
	return new(presentation.Service), nil
}
