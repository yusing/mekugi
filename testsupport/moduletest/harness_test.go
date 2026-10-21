package moduletest_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestFrontendBinderConformance(t *testing.T) {
	for _, factory := range []struct {
		name string
		new  func() binding.Binder
	}{
		{"codex", func() binding.Binder { return new(binding.Registry) }},
		{"scripted", func() binding.Binder { return new(moduletest.Scripted) }},
	} {
		t.Run(factory.name, func(t *testing.T) {
			binder := factory.new()
			if got := binder.Capability(capability.ExecutableFrontend); got.State != capability.Available {
				t.Fatalf("executable capability=%v", got)
			}
			if got := binder.Capability("unimplemented"); got.State == capability.Available || got.Reason == "" {
				t.Fatalf("unimplemented capability=%v", got)
			}
			valid := command.Spec{Name: "read", Description: "bounded read"}
			for _, invalid := range []command.Spec{{}, {Name: "read"}, {Name: "../read", Description: "bounded read"}} {
				if err := binder.Frontend(invalid); err == nil {
					t.Fatalf("accepted invalid command %+v", invalid)
				}
			}
			if err := binder.Frontend(valid); err != nil {
				t.Fatal(err)
			}
			if err := binder.Frontend(valid); err == nil {
				t.Fatal("duplicate command accepted")
			}
		})
	}
}

func TestHarnessOwnsTemporaryResourceLifecycle(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "owned-resource")
	type service struct{ path string }
	var events []string
	t.Run("module", func(t *testing.T) {
		script := new(moduletest.Scripted)
		spec := command.Contribution{Name: "read", Description: "bounded read"}
		descriptor := module.Descriptor[string, *service, binding.Bindings[*service], module.NoViews]{
			ID: "resource", Lifetime: module.Invocation, Resources: []string{"temporary resource"}, Contributes: module.Contributions[*service]{Commands: []command.Definition[*service]{{Spec: spec.Spec}}},
			New: func(dir string, slots module.Slots) (*service, error) {
				if dir != directory || !slices.Equal(slots.Guidance, []guidance.Section{{Command: "read", Text: "bounded read"}}) {
					t.Fatalf("dependencies=%q slots=%v", dir, slots)
				}
				events = append(events, "construct")
				return &service{filepath.Join(dir, "owned-resource")}, nil
			},
			Bindings: binding.Bindings[*service]{Requires: []capability.Requirement{{Mechanism: capability.ExecutableFrontend, Required: true}}, Frontends: true},
			Start: func(ctx context.Context, s *service) error {
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				events = append(events, "start")
				return os.WriteFile(s.path, []byte("resource"), 0600)
			},
			Stop: func(ctx context.Context, s *service) error {
				if ctx.Err() != nil {
					t.Error(ctx.Err())
				}
				events = append(events, "stop")
				return os.Remove(s.path)
			},
		}
		got := moduletest.New(t, descriptor, directory, module.Slots{Guidance: []guidance.Section{{Command: "read", Text: "bounded read"}}}, script)
		if got.path != path || !slices.Equal(script.Commands(), []command.Spec{spec.Spec}) {
			t.Fatalf("service=%+v commands=%v", got, script.Commands())
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("resource survived cleanup: %v", err)
	}
	if !slices.Equal(events, []string{"construct", "start", "stop"}) {
		t.Fatalf("lifecycle=%v", events)
	}
}
