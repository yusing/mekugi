package agentguidance

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
)

type Deps struct{}

func newService(_ Deps, slots module.Slots) (*policy.Service, error) {
	return policy.New(slots.Guidance)
}
