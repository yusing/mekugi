package launch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
)

func testComposition(t *testing.T) *composition.Runtime {
	t.Helper()
	runtime, err := composition.Frontends(t.Context(), false, true, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

func TestCompositionFrontendCatalog(t *testing.T) {
	runtime := testComposition(t)
	directory := t.TempDir()
	registry, err := frontends.New(t.Context(), frontends.Config{
		Commands:        runtime.Commands,
		ReplayDirectory: filepath.Join(directory, "replay"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	var names []string
	entries, err := os.ReadDir(registry.FrontendDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"mchanges", "mreload"}) {
		t.Fatalf("composed frontend catalog: %v", names)
	}
}
