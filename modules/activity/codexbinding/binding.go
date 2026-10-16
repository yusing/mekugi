package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/transcript"
)

type Bindings = binding.Bindings[*feed.Service]

var Codex = Bindings{
	Requires: []capability.Requirement{{Mechanism: capability.Observation, Required: true}},
	Observation: func(service *feed.Service) binding.Observation {
		return binding.Observation{NewTranscript: func(store *scope.Store) observation.Transcript {
			// Activity can be omitted from an offline or standalone UI.
			// Transient text correlation still needs no output-retention owner.
			if service == nil {
				return new(transcript.State)
			}
			state := service.Transcript(store)
			*state = transcript.State{}
			return state
		}}
	},
}
