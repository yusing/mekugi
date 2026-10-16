package binding

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/backends/codex/rollout"
)

type Rollout = rollout.Inspection
type RolloutCall = rollout.Call
type RolloutEvent = rollout.InspectionEvent
type RolloutItem = rollout.ToolItem
type RolloutReader func(context.Context, string, func(RolloutEvent) error) (Rollout, error)

// Rollout attaches an offline decoder without starting a backend process.
func (*Registry) Rollout(attach func(RolloutReader) error) error {
	if attach == nil {
		return errors.New("missing rollout consumer")
	}
	return attach(rollout.Inspect)
}
