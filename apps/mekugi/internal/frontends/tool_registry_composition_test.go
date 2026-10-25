package frontends

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestComposedFrontendCatalogReachesAuthenticatedRegistry(t *testing.T) {
	all := testCommands()
	for _, commands := range [][]command.Contribution{nil, all[:1], all} {
		t.Run(strconv.Itoa(len(commands)), func(t *testing.T) {
			directory := t.TempDir()
			registry, err := New(t.Context(), Config{Commands: commands, Guidance: testGuidance(), RuntimeDirectory: filepath.Join(directory, "runtime"), ReplayDirectory: filepath.Join(directory, "replay")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Error(err)
				}
			})
			if len(registry.Frontends) != len(commands) {
				t.Fatalf("selected %d commands, exposed %d frontends", len(commands), len(registry.Frontends))
			}
			for _, spec := range all {
				selected := slices.ContainsFunc(commands, func(c command.Contribution) bool { return c.Spec == spec.Spec })
				if strings.Contains(registry.FrontendGuidance.Descriptions, "<tool name=\""+spec.Name+"\">") != selected {
					t.Fatalf("%s guidance does not match selection", spec.Name)
				}
			}
			if len(commands) == 0 && registry.FrontendGuidance != (binding.FrontendGuidance{}) {
				t.Fatal("empty catalog projects guidance")
			}

		})
	}
}

func TestComposedCatalogRejectsInvalidExposure(t *testing.T) {
	for _, name := range []string{"../escaped-frontend", "/absolute", `..\escaped`, ".", "..", "", "unimplemented"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			runtime := filepath.Join(directory, "runtime")
			contribution := testCommands()[0]
			contribution.Name = name
			if name == "unimplemented" {
				contribution.Run = nil
			}
			registry, err := New(t.Context(), Config{Commands: []command.Contribution{contribution}, Guidance: testGuidance(), RuntimeDirectory: runtime, ReplayDirectory: filepath.Join(directory, "replay")})
			if err == nil {
				_ = registry.Close()
				t.Fatal("published invalid frontend")
			}
			entries, readErr := os.ReadDir(runtime)
			if readErr != nil && !os.IsNotExist(readErr) || len(entries) != 0 {
				t.Fatalf("failed publication left owned artifacts: %v, %v", entries, readErr)
			}
		})
	}
}
