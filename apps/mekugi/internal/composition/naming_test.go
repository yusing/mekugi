package composition

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionnaming"
)

func TestCompositionSessionNamingRemoval(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var c Composition
		if enabled {
			c.Add(sessionnaming.Module, func() sessionnaming.Deps { return sessionnaming.Deps{} })
		}
		runtime, err := c.Activate(t.Context(), new(binding.Registry))
		if err != nil {
			t.Fatal(err)
		}
		if (runtime.Naming != nil) != enabled {
			t.Fatalf("enabled=%v, registered=%v", enabled, runtime.Naming != nil)
		}
		if err := runtime.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
