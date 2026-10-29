package agentguidance

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct {
	ChildReplySummary bool
	Changes           changes.Service
	History           *history.Service
}

func newService(deps Deps, slots module.Slots) (*policy.Service, error) {
	service, err := policy.New(slots.Guidance)
	if err == nil {
		service.ChildReplySummary = deps.ChildReplySummary
		service.Changes, service.History = deps.Changes, deps.History
	}
	return service, err
}
