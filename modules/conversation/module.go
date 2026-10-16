package conversation

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/conversation/presentation"
)

var Module = module.Descriptor[Deps, *presentation.Service, module.NoBindings]{
	ID: "conversation", Summary: "Own conversation drafts, prompts, queues, and side answers",
	Lifetime: module.AttachedSession, New: newService,
}
