package outputdeduplication

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/outputdeduplication/codexbinding"
	"github.com/yusing/mekugi/modules/outputdeduplication/internal/projection"
)

var Module = module.Descriptor[Deps, projection.Service, codexbinding.Bindings, module.NoViews]{
	ID: "outputdeduplication", Summary: "Project repeated model-input output as visible references", Lifetime: module.Stateless,
	New: newService, Bindings: codexbinding.Codex,
}
