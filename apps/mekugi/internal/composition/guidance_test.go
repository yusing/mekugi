package composition

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/fileread"
)

func TestCompositionGuidanceRemovalKeepsCommands(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var c Composition
		c.Add(fileread.Module, func() fileread.Deps { return fileread.Deps{} })
		if enabled {
			c.Add(agentguidance.Module, func() agentguidance.Deps { return agentguidance.Deps{} })
		}
		runtime, err := c.Activate(t.Context(), new(binding.Registry))
		if err != nil {
			t.Fatal(err)
		}
		count := 1
		if enabled {
			count++
		}
		if (runtime.Guidance != nil) != enabled || len(runtime.Commands) != count || runtime.Commands[0].Name != "mcat" || runtime.Commands[0].Run == nil {
			t.Fatalf("guidance=%v commands=%+v", runtime.Guidance != nil, runtime.Commands)
		}
		if err := runtime.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
