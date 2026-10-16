package composition

import (
	"context"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
)

func TestConstructedCommandBindsServiceAndDependenciesForEarlierHost(t *testing.T) {
	type dependency struct{ value string }
	type service struct {
		dependency *dependency
		value      string
		started    bool
	}
	var c Composition
	hostDescriptor := descriptor("host")
	hostDescriptor.HostsCommands = true
	host := c.Add(hostDescriptor, emptyDeps)
	base := c.Add(module.Descriptor[struct{}, *dependency, module.NoBindings]{
		ID: "dependency", Lifetime: module.Stateless,
		New: func(struct{}, module.Slots) (*dependency, error) {
			return &dependency{value: "constructed dependency"}, nil
		},
	}, emptyDeps)
	spec := command.Spec{Name: "constructed", Description: "constructed service command"}
	calls := 0
	constructed := c.Add(module.Descriptor[*dependency, *service, binding.Bindings[*service]]{
		ID: "contributor", Requires: []module.ID{"dependency"}, Lifetime: module.Invocation,
		Contributes: module.Contributions[*service]{Commands: []command.Definition[*service]{{
			Spec: spec,
			Run: func(s *service, _ context.Context, request command.Request) (command.Output, error) {
				calls++
				if !s.started || s.dependency != base.Value() {
					t.Fatal("command did not receive the started service and its constructed dependency")
				}
				return command.Output{Stdout: s.value + ":" + s.dependency.value + ":" + request.Args[0]}, nil
			},
		}}},
		New: func(d *dependency, _ module.Slots) (*service, error) {
			if len(host.Value().commands) != 1 || host.Value().commands[0].Spec != spec {
				t.Fatal("earlier host did not receive the later contributor before its construction")
			}
			return &service{dependency: d, value: "constructed service"}, nil
		},
		Start:    func(_ context.Context, s *service) error { s.started = true; return nil },
		Stop:     func(_ context.Context, s *service) error { s.started = false; return nil },
		Bindings: binding.Bindings[*service]{Frontends: []command.Spec{spec}},
	}, base.Value)
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if calls != 0 {
		t.Fatalf("command executed during activation: %d calls", calls)
	}
	// Mutating the constructed values also rejects closures over zero values or copies.
	constructed.Value().value = "live service"
	base.Value().value = "live dependency"
	for _, contribution := range []command.Contribution{host.Value().commands[0], runtime.Commands[0]} {
		output, err := contribution.Run(t.Context(), command.Request{Args: []string{"argument"}})
		if err != nil || output.Stdout != "live service:live dependency:argument" {
			t.Fatalf("constructed command output=%+v err=%v", output, err)
		}
	}
	if calls != 2 {
		t.Fatalf("command calls=%d, want 2", calls)
	}
}
