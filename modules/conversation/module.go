package conversation

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

var Module = module.Descriptor[Deps, composer.Service, module.NoBindings]{
	ID: "conversation", Summary: "Own conversation drafts, questions, and approval presentation",
	Lifetime: module.AttachedSession, New: newService,
}
