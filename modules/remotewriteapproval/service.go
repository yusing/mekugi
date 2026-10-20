package remotewriteapproval

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/remotewriteapproval/remote"
)

type Deps struct{}

func newService(Deps, module.Slots) (remote.Service, error) { return remote.Service{}, nil }
