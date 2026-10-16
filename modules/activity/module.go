package activity

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/activity/codexbinding"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/waittargets"
)

var Module = module.Descriptor[Deps, *feed.Service, codexbinding.Bindings]{
	ID: "activity", Summary: "Own host-operation feeds, roster derivation and retained output",
	Lifetime: module.AttachedSession, New: newService, Bindings: codexbinding.Codex,
	Contributes: module.Contributions[*feed.Service]{RecordKinds: []recordkind.Spec{waittargets.Record}},
}
