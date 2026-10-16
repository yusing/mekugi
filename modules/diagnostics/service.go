package diagnostics

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type Deps struct {
	History    *history.Service
	Inspection *query.Service
}

func newService(deps Deps, slots module.Slots) (*diagnostic.Service, error) {
	return diagnostic.New(deps.History, deps.Inspection, slots.DiagnosticSections)
}
