package agentguidance

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

type Deps struct{ ChildReplySummary bool }

func newService(deps Deps, slots module.Slots) (*policy.Service, error) {
	service, err := policy.New(slots.Guidance)
	if err == nil {
		service.ChildReplySummary = deps.ChildReplySummary
	}
	return service, err
}
