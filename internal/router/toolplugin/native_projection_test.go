package toolplugin

import (
	"testing"

	"github.com/yusing/mekugi/libraries/sourceanalysis/structure"
)

func TestNativeMCatTrailingOptionTerminator(t *testing.T) {
	file := nativeFixture(t, "fixture.txt", "read\n")
	result := nativeExecute(t, "mcat", file, "--")
	if result.ExitCode != 0 || result.Stdout != "read\n" {
		t.Fatalf("trailing terminator: %+v", result)
	}
}

func TestNativeSymbolRejectsNonFileLocationURIs(t *testing.T) {
	for _, uri := range []string{"relative.ts", "git:/document.ts", "file:///source.ts?revision=1"} {
		raw := nativeJSON(map[string]any{"uri": uri, "range": symbolRange{Start: structure.Position{0, 0}, End: structure.Position{0, 1}}})
		locations, err := parseNativeLocations([]byte(raw), "def")
		if err != nil || len(locations) != 1 || !locations[0].outside {
			t.Fatalf("non-file URI %q: %+v, %v", uri, locations, err)
		}
	}
}
