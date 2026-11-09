package sessioninspection

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessioninspection/codexbinding"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

var Module = module.Descriptor[Deps, *query.Service, codexbinding.Bindings, module.NoViews]{
	ID: "sessioninspection", Summary: "Query retained calls, outcomes and static corpus findings",
	Requires: []module.ID{"sessionhistory", "changehistory"}, Lifetime: module.Invocation, HostsInspection: true,
	New: newService, Bindings: codexbinding.Codex,
}
