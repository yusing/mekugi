package conversation

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/presentation"
)

var Module = module.Descriptor[Deps, *presentation.Service, codexbinding.Bindings, module.NoViews]{
	ID: "conversation", Summary: "Own conversation drafts, transcripts, prompts, queues, and side answers",
	Lifetime: module.AttachedSession, New: newService, Bindings: codexbinding.Codex,
}
