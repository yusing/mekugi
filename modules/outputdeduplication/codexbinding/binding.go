package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/modules/outputdeduplication/internal/projection"
)

type Bindings = binding.Bindings[projection.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.InputProjection, Required: true}},
	InputProjection: func(service projection.Service) binding.InputProjection {
		return binding.InputProjection{
			Name: "outputdeduplication",
			Prepare: func(input []byte) binding.PreparedInputProjection {
				parts := duplicateOutputHostParts(input)
				return func(input []byte) binding.ProjectionResult { return apply(service, input, parts) }
			},
		}
	},
}
