package binding

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/contracts/observation"
)

type Rollout = rollout.Inspection
type RolloutCall = rollout.Call
type RolloutEvent = rollout.InspectionEvent
type RolloutItem = rollout.ToolItem
type RolloutReader func(context.Context, string, func(RolloutEvent) error) (Rollout, error)

// RolloutSource exposes distinct decoded consumers of the same offline mechanism.
type RolloutSource struct {
	Inspect  RolloutReader
	Replay   func(context.Context, string, map[string]bool) (observation.Source, error)
	Identity func(string) (string, error)
}

// Rollout attaches an offline decoder without starting a backend process.
func (*Registry) Rollout(attach func(RolloutSource) error) error {
	if attach == nil {
		return errors.New("missing rollout consumer")
	}
	return attach(RolloutSource{Inspect: rollout.Inspect, Replay: rollout.Replay, Identity: rollout.HeaderIdentity})
}
