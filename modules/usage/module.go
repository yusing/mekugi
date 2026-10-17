package usage

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/modules/usage/accounting"
	usagepresentation "github.com/yusing/mekugi/modules/usage/presentation"
)

var Module = module.Descriptor[Deps, *accounting.Service, module.NoBindings]{
	ID: "usage", Summary: "Retain provider-authoritative thread usage, reference costs and throughput",
	Requires: []module.ID{"sessionhistory"}, Lifetime: module.Invocation,
	Resources:   []string{"on-demand retained-usage publication worker"},
	Contributes: module.Contributions[*accounting.Service]{RecordKinds: accounting.RecordKinds(), Presentation: []presentation.Status{usagepresentation.Throughput}},
	New:         newService, Start: startService, Stop: stopService,
}
