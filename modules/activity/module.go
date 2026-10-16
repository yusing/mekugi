package activity

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/activity/feed"
)

var Module = module.Descriptor[Deps, *feed.Service, module.NoBindings]{
	ID: "activity", Summary: "Retain host-operation content for activity presentation",
	Lifetime: module.AttachedSession, New: newService,
}
