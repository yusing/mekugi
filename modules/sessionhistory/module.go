package sessionhistory

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct{}

var Module = module.Descriptor[Deps, *history.Service, module.NoBindings]{
	ID:           "sessionhistory",
	Summary:      "Own retained session publication, namespaces, allocation, leases, and retention",
	Lifetime:     module.Invocation,
	HostsRecords: true,
	New:          newService,
}
