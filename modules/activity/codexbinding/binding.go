package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/feed"
)

type Bindings = binding.Bindings[*feed.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.Observation, Required: true}},
	Observation: func(service *feed.Service) binding.Observation {
		// Standalone and offline consumers use the same application-owned stores.
		if service == nil {
			service = new(feed.Service)
		}
		return binding.Observation{NewTranscript: func(store *scope.Store) observation.Transcript {
			return service.Transcript(store)
		}}
	},
}
