package composition

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
)

type testService struct {
	name     string
	guidance []guidance.Section
}
type testDescriptor = module.Descriptor[struct{}, *testService, binding.Bindings[*testService], module.NoViews]

func descriptor(id module.ID, specs ...command.Contribution) testDescriptor {
	definitions := make([]command.Definition[*testService], len(specs))
	for i, spec := range specs {
		definitions[i] = command.Definition[*testService]{Spec: spec.Spec, ObserveRead: spec.ObserveRead}
		if spec.Run != nil {
			definitions[i].Run = func(_ *testService, ctx context.Context, request command.Request) (command.Output, error) {
				return spec.Run(ctx, request)
			}
		}
	}

	return testDescriptor{
		ID: id, Lifetime: module.Stateless, Contributes: module.Contributions[*testService]{Commands: definitions},
		New: func(_ struct{}, slots module.Slots) (*testService, error) {
			return &testService{string(id), slots.Guidance}, nil
		},
		Bindings: binding.Bindings[*testService]{Frontends: len(specs) != 0},
	}
}
func emptyDeps() struct{} { return struct{}{} }
func frontend(name string) command.Contribution {
	return command.Contribution{Name: name, Description: "test frontend", Run: func(context.Context, command.Request) (command.Output, error) { return command.Output{}, nil }}
}

func TestCompositionTypedDependenciesAndCollectedSlots(t *testing.T) {
	var c Composition
	base := c.Add(descriptor("base"), emptyDeps)
	type deps struct{ Base *testService }
	host := c.Add(module.Descriptor[deps, *testService, binding.Bindings[*testService], module.NoViews]{
		ID: "host", Requires: []module.ID{"base"}, Lifetime: module.Stateless, HostsGuidance: true,
		New: func(d deps, slots module.Slots) (*testService, error) {
			if d.Base != base.Value() {
				t.Fatal("typed dependency lost identity")
			}
			return &testService{d.Base.name, slots.Guidance}, nil
		},
	}, func() deps { return deps{base.Value()} })
	contribution := frontend("later")
	later := descriptor("later", contribution)
	later.Contributes.Guidance = []guidance.Section{{Command: "later", Text: "read"}}
	c.Add(later, emptyDeps)
	registry := new(binding.Registry)
	runtime, err := c.Activate(t.Context(), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if len(base.Value().guidance) != 0 {
		t.Fatal("non-host received slots")
	}
	if host.Value().name != "base" || !slices.Equal(host.Value().guidance, later.Contributes.Guidance) || !slices.EqualFunc(registry.Commands(), runtime.Commands, func(a command.Spec, b command.Contribution) bool { return a == b.Spec }) {
		t.Fatalf("host=%+v registered=%v runtime=%v", host.Value(), registry.Commands(), runtime.Commands)
	}
}

func TestCompositionFreezesFrontendDeclarations(t *testing.T) {
	var c Composition
	d := descriptor("reader", frontend("read"))
	c.Add(d, emptyDeps)
	d.Contributes.Commands[0].Name = "undeclared"
	registry := new(binding.Registry)
	runtime, err := c.Activate(t.Context(), registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if len(runtime.Commands) != 1 || runtime.Commands[0].Name != "read" || !slices.Equal(registry.Commands(), []command.Spec{runtime.Commands[0].Spec}) {
		t.Fatalf("runtime=%v registered=%v", runtime.Commands, registry.Commands())
	}
}

func TestCompositionValidatesBeforeConstruction(t *testing.T) {
	unavailable := capability.Mechanism("not-implemented")
	for _, tt := range []struct {
		name        string
		descriptors []testDescriptor
		reason      string
	}{
		{"missing", []testDescriptor{func() testDescriptor { d := descriptor("consumer"); d.Requires = []module.ID{"absent"}; return d }()}, "requires missing module absent"},
		{"order", []testDescriptor{func() testDescriptor { d := descriptor("consumer"); d.Requires = []module.ID{"dependency"}; return d }(), descriptor("dependency")}, "requires dependency before it"},
		{"duplicate module", []testDescriptor{descriptor("same"), descriptor("same")}, "duplicate module same"},
		{"duplicate frontend", []testDescriptor{descriptor("first", frontend("same")), descriptor("second", frontend("same"))}, "duplicate frontend"},
		{"invalid frontend", []testDescriptor{descriptor("invalid", command.Contribution{Name: "bad"})}, "inconsistent executable contribution"},
		{"missing frontend connection", []testDescriptor{func() testDescriptor {
			d := descriptor("missing-connection", frontend("read"))
			d.Bindings.Frontends = false
			return d
		}()}, "executable contributions and Codex frontend connection differ"},
		{"missing frontend contribution", []testDescriptor{func() testDescriptor {
			d := descriptor("missing-command")
			d.Bindings.Frontends = true
			return d
		}()}, "executable contributions and Codex frontend connection differ"},
		{"capability", []testDescriptor{func() testDescriptor {
			d := descriptor("required")
			d.Bindings.Requires = []capability.Requirement{{Mechanism: unavailable, Required: true}}
			return d
		}()}, "required not-implemented unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var c Composition
			constructed := 0
			for _, d := range tt.descriptors {
				d.New = func(struct{}, module.Slots) (*testService, error) { constructed++; return new(testService), nil }
				c.Add(d, emptyDeps)
			}
			registry := new(binding.Registry)
			if _, err := c.Activate(t.Context(), registry); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error=%v, want %q", err, tt.reason)
			}
			if constructed != 0 || len(registry.Commands()) != 0 {
				t.Fatalf("constructed=%d registrations=%v before validation", constructed, registry.Commands())
			}
		})
	}
}

func TestCompositionZeroAndLeaveOneOutContributions(t *testing.T) {
	for _, omit := range []string{"none", "first", "second", "both"} {
		t.Run(omit, func(t *testing.T) {
			var c Composition
			hostDescriptor := descriptor("host")
			hostDescriptor.Bindings.Requires = []capability.Requirement{{Mechanism: "optional-observation"}}
			c.Add(hostDescriptor, emptyDeps)
			var want []command.Contribution
			for _, name := range []string{"first", "second"} {
				if omit == name || omit == "both" {
					continue
				}
				spec := frontend(name)
				want = append(want, spec)
				c.Add(descriptor(module.ID(name), spec), emptyDeps)
			}
			runtime, err := c.Activate(t.Context(), new(binding.Registry))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.EqualFunc(runtime.Commands, want, func(a, b command.Contribution) bool { return a.Spec == b.Spec }) {
				t.Fatalf("runtime=%v want=%v", runtime.Commands, want)
			}
			if len(runtime.Gaps) != 1 || runtime.Gaps[0].Module != "host" || runtime.Gaps[0].Result.State != capability.Unsupported {
				t.Fatalf("optional gap=%v", runtime.Gaps)
			}
			if err := runtime.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompositionRejectsIncompleteFactoriesBeforeConstruction(t *testing.T) {
	for _, defect := range []string{"constructor", "dependencies", "resource lifecycle"} {
		t.Run(defect, func(t *testing.T) {
			var c Composition
			constructed := false
			first := descriptor("first")
			first.New = func(struct{}, module.Slots) (*testService, error) {
				constructed = true
				return new(testService), nil
			}
			c.Add(first, emptyDeps)
			invalid, deps := descriptor("invalid"), emptyDeps
			switch defect {
			case "constructor":
				invalid.New = nil
			case "dependencies":
				deps = nil
			case "resource lifecycle":
				invalid.Lifetime = module.Invocation
				invalid.Resources = []string{"open endpoint"}
			}
			c.Add(invalid, deps)
			registry := new(binding.Registry)
			if _, err := c.Activate(t.Context(), registry); err == nil {
				t.Fatal("accepted incomplete declaration")
			}
			if constructed || len(registry.Commands()) != 0 {
				t.Fatal("validation happened after construction or registration")
			}
		})
	}
}

func TestCompositionLifecycleReverseCleanup(t *testing.T) {
	for _, failStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "rollback"}[failStart], func(t *testing.T) {
			var c Composition
			var events []string
			failure := errors.New("start failed")
			for _, name := range []string{"first", "second"} {
				d := descriptor(module.ID(name))
				d.Lifetime = module.Invocation
				d.Resources = []string{"temporary resource"}
				d.Start = func(_ context.Context, s *testService) error {
					events = append(events, "start "+s.name)
					if failStart && s.name == "second" {
						return failure
					}
					return nil
				}
				d.Stop = func(ctx context.Context, s *testService) error {
					if ctx.Err() != nil {
						t.Error("cleanup context canceled")
					}
					events = append(events, "stop "+s.name)
					return nil
				}
				c.Add(d, emptyDeps)
			}
			runtime, err := c.Activate(t.Context(), new(binding.Registry))
			if failStart {
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := runtime.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := runtime.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			want := []string{"start first", "start second", "stop second", "stop first"}
			if !slices.Equal(events, want) {
				t.Fatalf("events=%v want=%v", events, want)
			}
		})
	}
}

func TestCompositionRejectsUnimplementedCommandBeforeConstruction(t *testing.T) {
	spec := frontend("missing")
	spec.Run = nil
	d := descriptor("tool", spec)
	d.New = func(struct{}, module.Slots) (*testService, error) {
		t.Fatal("constructed unimplemented command")
		return nil, nil
	}
	var c Composition
	c.Add(d, emptyDeps)
	if _, err := c.Activate(t.Context(), new(binding.Registry)); err == nil || !strings.Contains(err.Error(), "no compiled implementation") {
		t.Fatalf("activation: %v", err)
	}
}
