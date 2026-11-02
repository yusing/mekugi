package moduletest_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestFrontendBinderConformance(t *testing.T) {
	for _, factory := range []struct {
		name string
		new  func() binding.Registrar
	}{
		{"codex", func() binding.Registrar { return new(binding.Registry) }},
		{"scripted", func() binding.Registrar { return new(moduletest.Scripted) }},
	} {
		t.Run(factory.name, func(t *testing.T) {
			binder := factory.new()
			valid := command.Spec{Name: "read", Description: "bounded read"}
			if err := binder.Frontend(valid); err != nil {
				t.Fatal(err)
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
			Bindings: binding.Bindings[*service]{Frontends: true},
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
