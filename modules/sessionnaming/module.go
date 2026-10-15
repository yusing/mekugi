package sessionnaming

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionnaming/codexbinding"
	"github.com/yusing/mekugi/modules/sessionnaming/internal/naming"
)

var Module = module.Descriptor[Deps, naming.Service, codexbinding.Bindings]{
	ID: "sessionnaming", Summary: "Generate optional thread names while preserving manual authority", Lifetime: module.HostOperation,
	New: newService, Bindings: codexbinding.Codex,
}
