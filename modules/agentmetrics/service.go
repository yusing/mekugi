package agentmetrics

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

type Deps struct{}

func newService(Deps, module.Slots) (*evidence.Service, error) { return new(evidence.Service), nil }
