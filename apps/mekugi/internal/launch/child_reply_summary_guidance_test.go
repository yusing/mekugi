package launch

import (
	jsonv1 "encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestChildReplySummaryGuidanceInvocation(t *testing.T) {
	var inherited string
	for _, enabled := range []bool{true, false, true} {
		options, err := launchoptions.Parse(nil)
		if err != nil {
			t.Fatal(err)
		}
		options = options.ResolveFeatures(launchoptions.Features{ChildReplySummary: new(enabled)})
		runtime, err := composition.Frontends(t.Context(), false, options.ChildReplySummary, "")
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close(t.Context())
		dir := t.TempDir()
		registry, err := frontends.New(t.Context(), frontends.Config{Commands: runtime.Commands, Guidance: runtime.Guidance, RuntimeDirectory: filepath.Join(dir, "runtime"), ReplayDirectory: filepath.Join(dir, "replay")})
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		for _, history := range []string{"fresh assembly", "inherited assembly"} {
			base := "Preserve caller exact-output policy."
			if history == "inherited assembly" {
				base += "\n" + inherited
			}
			fields := map[string]jsonv1.RawMessage{
				"instructions": responses.MustMarshalJSON(base),
				"input":        responses.MustMarshalJSON([]any{testCodeModeAdditionalTools(testCodeModeDescription)}),
			}
			for range 2 {
				if _, err := binding.PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), registry.FrontendGuidance, runtime.Guidance); err != nil {
					t.Fatal(err)
				}
			}
			got := responses.StringField(fields, "instructions")
			want := 0
			if enabled {
				want = 1
			}
			if strings.Count(got, "For substantive child-to-parent") != want || !strings.Contains(got, "Preserve caller exact-output policy.") || !strings.Contains(got, "Codex owns stock editing") {
				t.Fatalf("enabled=%v history=%q guidance=%s", enabled, history, got)
			}
			if runtime.Terminal.ChildReplySummary != enabled {
				t.Fatal("guidance and presentation diverged")
			}
		}
		inherited = registry.FrontendGuidance.Workflow
	}
}
