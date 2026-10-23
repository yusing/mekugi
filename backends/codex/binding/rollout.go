package binding

import (
	"context"
	"encoding/json/jsontext"
	"errors"

	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/contracts/observation"
)

type Rollout = rollout.Inspection
type RolloutCall = rollout.Call
type RolloutEvent = rollout.InspectionEvent
type RolloutItem = rollout.ToolItem
type RuntimeContext = rollout.Runtime
type AdmissionSettings = rollout.Admission
type ForkSource = rollout.PrefixSource
type ForkBoundary = rollout.PrefixBoundary
type ForkResolver = rollout.PrefixResolver

func ReadForkPrefix(ctx context.Context, source ForkSource, boundary ForkBoundary, resolve ForkResolver) ([]byte, error) {
	return rollout.ReadPrefix(ctx, source, boundary, resolve)
}

// ReadRuntimeContext consumes only a host-selected path and exact turn identity.
// The caller retains responsibility for admitting the thread into its invocation.
func ReadRuntimeContext(thread, path, turn string) (RuntimeContext, error) {
	return rollout.ReadRuntime(thread, path, turn)
}

func ReadAdmissionSettings(thread, path string) (AdmissionSettings, error) {
	return rollout.ReadAdmission(thread, path)
}

func ConfirmHistoryInjection(ctx context.Context, thread, path string, expected jsontext.Value) error {
	return rollout.ConfirmInjection(ctx, thread, path, expected)
}

func ConfirmRetainedOutput(ctx context.Context, thread, path, namespace, name, output string) error {
	return rollout.ConfirmRetainedOutput(ctx, thread, path, namespace, name, output)
}

func ConfirmTerminalTurn(ctx context.Context, thread, path, turn string) error {
	return rollout.ConfirmTerminalTurn(ctx, thread, path, turn)
}

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
