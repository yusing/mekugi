package sessionhistory

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func newService(deps Deps, slots module.Slots) (*history.Service, error) {
	return history.NewService(append(slots.RecordKinds, deps.RecordKinds...))
}
