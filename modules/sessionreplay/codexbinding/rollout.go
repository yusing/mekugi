package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

type Bindings = binding.Bindings[*playback.Service]

var Codex = Bindings{
	Rollout: func(service *playback.Service, source binding.RolloutSource) error {
		return service.BindSource(playback.Decoder{Read: source.Replay, Identity: source.Identity})
	},
}
