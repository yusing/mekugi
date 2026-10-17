package activity

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/activity/feed"
)

type Deps struct{}

func newService(_ Deps, slots module.Slots) (*feed.Service, error) {
	return feed.New(slots.ActivityDetails)
}
