package commandsegments

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct {
	History *history.Service
	Live    bool
}

var Module = module.Descriptor[Deps, *segments.Service, module.NoBindings]{
	ID: "commandsegments", Summary: "Receive, retain, and restore command-segment observations",
	Requires: []module.ID{"sessionhistory"}, Lifetime: module.Invocation,
	Resources:   []string{"optional private report FIFOs and consumer tasks"},
	Contributes: module.Contributions[*segments.Service]{RecordKinds: reports.RecordKinds()},
	New:         newService, Start: startService, Stop: stopService,
}
