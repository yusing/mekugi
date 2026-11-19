package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

type Bindings = binding.Bindings[*compact.CaptureService]

var Codex = Bindings{Frontends: true}
