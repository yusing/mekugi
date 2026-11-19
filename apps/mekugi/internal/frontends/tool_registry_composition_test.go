package frontends

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestComposedFrontendCatalogReachesAuthenticatedRegistry(t *testing.T) {
	all := testCommands()
	for _, commands := range [][]command.Contribution{nil, all[:1], all} {
		t.Run(strconv.Itoa(len(commands)), func(t *testing.T) {
			directory := t.TempDir()
			registry, err := New(t.Context(), Config{Commands: commands, ReplayDirectory: filepath.Join(directory, "replay")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Error(err)
				}
			})
			manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, manifestFilename))
			if err != nil {
				t.Fatal(err)
			}

			expected := make([]string, len(commands))
			for i, command := range commands {
				expected[i] = command.Name
			}
			if !slices.Equal(manifest.Tools, expected) {
				t.Fatalf("worker manifest selected %v, want %v", manifest.Tools, expected)
			}
		})
	}
}

func TestComposedCatalogRejectsInvalidExposure(t *testing.T) {
	for _, name := range []string{"../escaped-frontend", "/absolute", `..\escaped`, ".", "..", "", "unimplemented"} {
		t.Run(name, func(t *testing.T) {
			contribution := testCommands()[0]
			contribution.Name = name
			if name == "unimplemented" {
				contribution.Run = nil
			}
			if err := ValidateCatalog([]command.Contribution{contribution}); err == nil {
				t.Fatal("accepted invalid frontend")
			}

		})
	}
}

func TestCatalogRejectsDuplicateCommands(t *testing.T) {
	contribution := testCommands()[0]
	if err := ValidateCatalog([]command.Contribution{contribution, contribution}); err == nil {
		t.Fatal("accepted duplicate frontend")
	}
}
