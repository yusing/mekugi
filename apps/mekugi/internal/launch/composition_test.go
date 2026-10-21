package launch

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
)

func testComposition(t *testing.T) *composition.Runtime {
	t.Helper()
	runtime, err := composition.Frontends(t.Context(), false, true, "")
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
	if len(runtime.Gaps) != 0 {
		t.Fatalf("composed capability gaps: %v", runtime.Gaps)
	}
	directory := t.TempDir()
	registry, err := frontends.New(t.Context(), frontends.Config{
		Commands: runtime.Commands, Guidance: runtime.Guidance,
		RuntimeDirectory: filepath.Join(directory, "runtime"), ReplayDirectory: filepath.Join(directory, "replay"),
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
	for name := range registry.Frontends {
		names = append(names, name)
		if strings.Count(registry.FrontendGuidance.Descriptions, "<tool name=\""+name+"\">") != 1 {
			t.Fatalf("%s missing or repeated in authenticated guidance", name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"inspect_file", "mcat", "mchanges", "mread", "mrun", "msymbol"}) {
		t.Fatalf("composed frontend catalog: %v", names)
	}
}
