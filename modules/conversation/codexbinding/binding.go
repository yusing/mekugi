package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/presentation"
)

type Bindings = binding.Bindings[*presentation.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.Observation, Required: true}},
	Observation: func(service *presentation.Service) binding.Observation {
		return binding.Observation{NewTranscript: func(store *scope.Store) observation.Transcript {
			return service.Transcript(store)
		}}
	},
}
