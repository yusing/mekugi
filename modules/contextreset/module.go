package contextreset

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
)

var Module = module.Descriptor[Deps, guidance.Service, module.NoBindings]{
	ID: "contextreset", Summary: "Collect bounded instruction sources for future context reset",
	Requires: []module.ID{"fileread", "sourceoutline"}, Lifetime: module.Stateless,
	New: newService,
}
