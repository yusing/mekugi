package notifications

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

type Deps struct{}

func newService(Deps, module.Slots) (*notice.Service, error) { return new(notice.Service), nil }
