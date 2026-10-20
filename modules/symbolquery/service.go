package symbolquery

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/symbolquery/query"
)

type Deps struct{}

func newService(Deps, module.Slots) (query.Service, error) { return query.Service{}, nil }
