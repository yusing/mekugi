package composition

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/remotewriteapproval"
	"github.com/yusing/mekugi/modules/sudoapproval"
)

func TestCompositionApprovalRemoval(t *testing.T) {
	for _, tc := range []struct {
		name         string
		remote, sudo bool
	}{
		{"both", true, true},
		{"remote only", true, false},
		{"sudo only", false, true},
		{"neither", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Composition
			if tc.remote {
				c.Add(remotewriteapproval.Module, func() remotewriteapproval.Deps { return remotewriteapproval.Deps{} })
			}
			if tc.sudo {
				c.Add(sudoapproval.Module, func() sudoapproval.Deps { return sudoapproval.Deps{} })
			}
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
			seen := make(map[binding.ShellGuardKind]bool)
			for _, guard := range runtime.Guards {
				if _, duplicate := seen[guard.Kind]; duplicate {
					t.Fatalf("duplicate runtime guard %q", guard.Kind)
				}
				seen[guard.Kind] = true
			}
			if seen[binding.RemoteWrite] != tc.remote || seen[binding.Sudo] != tc.sudo || len(registry.RegisteredGuards()) != len(runtime.Guards) {
				t.Fatalf("remote=%v sudo=%v: runtime=%+v registry=%+v", tc.remote, tc.sudo, runtime.Guards, registry.RegisteredGuards())
			}
		})
	}
}
