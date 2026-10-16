package diagnostics

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
)

var Module = module.Descriptor[Deps, *diagnostic.Service, module.NoBindings]{
	ID: "diagnostics", Summary: "Retain and inspect private diagnostic evidence",
	Requires: []module.ID{"sessionhistory", "sessioninspection"}, Lifetime: module.Invocation,
	HostsDiagnosticSections: true, New: newService,
	Contributes: module.Contributions[*diagnostic.Service]{RecordKinds: []recordkind.Spec{diagnostic.Failures}},
}
