package sessioninspection

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type Deps struct{ History *history.Service }

func newService(deps Deps, slots module.Slots) (*query.Service, error) {
	return query.New(deps.History, slots.Inspection)
}
