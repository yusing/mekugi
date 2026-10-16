package conversation

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

var Module = module.Descriptor[Deps, composer.Service, module.NoBindings]{
	ID: "conversation", Summary: "Own editable conversation drafts and attachment identity",
	Lifetime: module.AttachedSession, New: newService,
}
