// Package moduletest constructs a module with explicit real dependencies and a
// scripted Codex binder. It is imported only by tests.
package moduletest

import (
	"context"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
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
func New[D, S any](t *testing.T, descriptor module.Descriptor[D, S, binding.Bindings[S]], deps D, slots module.Slots, script *Scripted) S {
	t.Helper()
	for _, requirement := range descriptor.Bindings.Requires {
		result := script.Capability(requirement.Mechanism)
		if requirement.Required && result.State != capability.Available {
			t.Fatalf("%s: %s unavailable: %s", descriptor.ID, requirement.Mechanism, result.Reason)
		}
	}
	service, err := descriptor.New(deps, slots)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Bindings.Register != nil {
		if err := descriptor.Bindings.Register(script, service); err != nil {
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
