package query

import (
	"encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/libraries/sourceanalysis/structure"
)

func TestNativeSymbolRejectsNonFileLocationURIs(t *testing.T) {
	for _, uri := range []string{"relative.ts", "git:/document.ts", "file:///source.ts?revision=1"} {
		raw, err := json.Marshal(map[string]any{"uri": uri, "range": symbolRange{Start: structure.Position{0, 0}, End: structure.Position{0, 1}}})
		if err != nil {
			t.Fatal(err)
		}
		locations, err := parseLocations(raw, "def")
		if err != nil || len(locations) != 1 || !locations[0].outside {
			t.Fatalf("non-file URI %q: %+v, %v", uri, locations, err)
		}
	}
}
