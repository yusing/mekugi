package transportdiagnostics

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type Deps struct{}

func newService(Deps, module.Slots) (*capture.Service, error) { return new(capture.Service), nil }
