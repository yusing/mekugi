package commandsegments

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct{ History *history.Service }

var Module = module.Descriptor[Deps, *reports.Service, module.NoBindings]{
	ID: "commandsegments", Summary: "Retain and restore completed command-segment observations",
	Requires: []module.ID{"sessionhistory"}, Lifetime: module.Stateless,
	Contributes: module.Contributions[*reports.Service]{RecordKinds: reports.RecordKinds()},
	New:         newService,
}
