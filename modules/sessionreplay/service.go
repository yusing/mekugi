package sessionreplay

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

type Deps struct{ History *history.Service }

func newService(deps Deps, slots module.Slots) (*playback.Service, error) {
	return playback.New(deps.History, slots.ReplayTiming)
}
