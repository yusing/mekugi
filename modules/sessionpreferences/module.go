package sessionpreferences

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

var Module = module.Descriptor[Deps, *preferences.Service, module.NoBindings]{
	ID: "sessionpreferences", Summary: "Retain applied settings and renderer preferences for a workspace and thread",
	Requires: []module.ID{"sessionhistory"}, Lifetime: module.Invocation,
	New: newService,
}
