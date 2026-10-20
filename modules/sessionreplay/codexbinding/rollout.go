package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

type Bindings = binding.Bindings[*playback.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.Rollout, Required: true}},
	Rollout: func(service *playback.Service, source binding.RolloutSource) error {
		return service.BindSource(playback.Decoder{Read: source.Replay, Identity: source.Identity})
	},
}
