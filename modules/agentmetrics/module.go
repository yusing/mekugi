package agentmetrics

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

var Module = module.Descriptor[Deps, *evidence.Service, module.NoBindings]{
	ID: "agentmetrics", Summary: "Measure reader invocations, observed edits and recorded host completion",
	Lifetime: module.Stateless, New: newService,
}
