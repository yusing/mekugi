package sessionreplay

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

type Deps struct {
	History *history.Service
	Timing  replaytiming.Reader
}

func newService(deps Deps, _ module.Slots) (*playback.Service, error) {
	return playback.New(deps.History, deps.Timing)
}
