package sessioninspection

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type Deps struct{}

func newService(_ Deps, slots module.Slots) (*query.Service, error) {
	return query.New(slots.Inspection)
}
