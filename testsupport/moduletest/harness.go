// Package moduletest constructs a module with explicit real dependencies and a
// scripted Codex binder. It is imported only by tests.
package moduletest

import (
	"context"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
)

type Scripted struct {
	binding.Registry
	Capabilities map[capability.Mechanism]capability.Result
}

func (s *Scripted) Capability(mechanism capability.Mechanism) capability.Result {
	if result, ok := s.Capabilities[mechanism]; ok {
		return result
	}
	if mechanism == capability.ExecutableFrontend {
		return capability.Result{State: capability.Available, Reason: "scripted executable registration"}
	}
	return capability.Result{State: capability.Unknown, Reason: "not scripted"}
}

// New uses the caller's real dependencies, including temporary-storage owners.
// Composition graph and startup rollback are tested by the application owner.
func New[D, S any, B interface {
	module.NoBindings | binding.Bindings[S]
}](t *testing.T, descriptor module.Descriptor[D, S, B], deps D, slots module.Slots, script *Scripted) S {
	t.Helper()
	backend, _ := any(descriptor.Bindings).(binding.Bindings[S])
	for _, requirement := range backend.Requires {
		result := script.Capability(requirement.Mechanism)
		if requirement.Required && result.State != capability.Available {
			t.Fatalf("%s: %s unavailable: %s", descriptor.ID, requirement.Mechanism, result.Reason)
		}
	}
	service, err := descriptor.New(deps, slots)
	if err != nil {
		t.Fatal(err)
	}
	if backend.Register != nil {
		if err := backend.Register(script, service); err != nil {
			t.Fatal(err)
		}
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
