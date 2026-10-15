package commandsegments

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
)

func newService(deps Deps, _ module.Slots) (*reports.Service, error) {
	return reports.New(deps.History)
}
