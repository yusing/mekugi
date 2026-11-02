package activity

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/activity/feed"
)

type Deps struct{}

func newService(_ Deps, _ module.Slots) (*feed.Service, error) {
	return new(feed.Service), nil
}
