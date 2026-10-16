package transportdiagnostics

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

var Module = module.Descriptor[Deps, *capture.Service, module.NoBindings]{
	ID: "transportdiagnostics", Summary: "Observe transport bytes, timing, capture health and provider-response evidence",
	Lifetime: module.Invocation, New: newService,
}
