// Package moduletest constructs a module with explicit real dependencies and a
// scripted Codex binder. It is imported only by tests.
package moduletest

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/yusing/mekugi/contracts/inspection"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
)

type Scripted struct {
	ReadRollout          binding.RolloutReader
	ReplaySource         binding.RolloutSource
	RolloutRegistrations int
	binding.Registry
}

// New uses the caller's real dependencies, including temporary-storage owners.
// Composition graph and startup rollback are tested by the application owner.
func New[D, S, V any, B interface {
	module.NoBindings | binding.Bindings[S]
}](t *testing.T, descriptor module.Descriptor[D, S, B, V], deps D, slots module.Slots, script *Scripted) S {
	t.Helper()
	backend, _ := any(descriptor.Bindings).(binding.Bindings[S])
	service, err := descriptor.New(deps, slots)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Register(script, service, descriptor.Contributes.Commands); err != nil {
		t.Fatal(err)
	}
	if descriptor.Stop != nil {
		t.Cleanup(func() {
			if err := descriptor.Stop(context.WithoutCancel(t.Context()), service); err != nil {
				t.Error(err)
			}
		})
	}
	if descriptor.Start != nil {
		if err := descriptor.Start(t.Context(), service); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

// BindCommand exposes a descriptor command against its constructed test service.
func BindCommand[S any](definition command.Definition[S], service S) command.Contribution {
	return command.Contribution{Spec: definition.Spec, ObserveRead: definition.ObserveRead,
		Run: func(ctx context.Context, request command.Request) (command.Output, error) {
			return definition.Run(service, ctx, request)
		},
	}
}

func (s *Scripted) Rollout(attach func(binding.RolloutSource) error) error {
	if attach == nil {
		return errors.New("missing rollout consumer")
	}
	s.RolloutRegistrations++
	source := s.ReplaySource
	source.Inspect = func(ctx context.Context, path string, observe func(binding.RolloutEvent) error) (binding.Rollout, error) {
		if s.ReadRollout == nil {
			return binding.Rollout{}, errors.New("rollout read is not scripted")
		}
		return s.ReadRollout(ctx, path, observe)
	}
	return attach(source)
}

func BindInspection[S any](definition inspection.Definition[S], service S) inspection.Contribution {
	var contribution inspection.Contribution
	if definition.NewAX != nil {
		contribution.NewAX = func() inspection.AXCollector { return definition.NewAX(service) }
	}
	if definition.ProviderUsage != nil {
		contribution.ProviderUsage = func(ctx context.Context, path string, filter inspection.UsageFilter) (map[string]inspection.Usage, error) {
			return definition.ProviderUsage(service, ctx, path, filter)
		}
	}
	if definition.RequestTiming != nil {
		contribution.RequestTiming = func(ctx context.Context, store inspection.RetainedReader, thread string) (jsontext.Value, error) {
			return definition.RequestTiming(service, ctx, store, thread)
		}
	}
	return contribution
}
