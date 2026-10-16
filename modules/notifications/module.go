package notifications

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

var Module = module.Descriptor[Deps, *notice.Service, module.NoBindings]{
	ID: "notifications", Summary: "Deliver bounded notices, failure details and recovery summaries",
	Lifetime: module.Invocation, New: newService,
}
