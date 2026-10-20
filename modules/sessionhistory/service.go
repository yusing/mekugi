package sessionhistory

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func newService(_ Deps, slots module.Slots) (*history.Service, error) {
	return history.NewService(slots.RecordKinds)
}
