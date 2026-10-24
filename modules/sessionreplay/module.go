package sessionreplay

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionreplay/codexbinding"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

var Module = module.Descriptor[Deps, *playback.Service, codexbinding.Bindings, module.NoViews]{
	ID: "sessionreplay", Summary: "Schedule retained observations with synthetic streaming and read-only playback controls",
	Requires: []module.ID{"activity", "agentguidance", "sessionhistory"}, Lifetime: module.Stateless,
	HostsReplayTiming: true, New: newService, Bindings: codexbinding.Codex,
}
