package toolplugin

import (
	"strings"
	"testing"
)

func TestNativeInspectPreservesLoneSurrogateNames(t *testing.T) {
	for _, source := range []string{`import "\uD800";`, `import '\u{D800}';`} {
		file := nativeFixture(t, "module.ts", source)
		result := nativeExecute(t, "inspect_file", "--json", file)
		if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"name":"\ud800"`) || strings.Contains(result.Stdout, "�") {
			t.Fatalf("surrogate import: %+v", result)
		}
	}
	file := nativeFixture(t, "keys.json", `{"\uD800":1,"a\uDFFF":2}`)
	result := nativeExecute(t, "inspect_file", "--json", file)
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"pointer":"/\ud800"`) || !strings.Contains(result.Stdout, `"pointer":"/a\udfff"`) {
		t.Fatalf("surrogate pointer: %+v", result)
	}
	result = nativeExecute(t, "inspect_file", file)
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `\ud800`) {
		t.Fatalf("compact surrogate pointer: %+v", result)
	}
	page, err := FormatOutput(t.Context(), []string{"1000", "read", nativeJSON(nativeReadRequest{Stdout: `[{"name":"\ud800"}]`, StdoutKind: "json"}), ""})
	if err != nil || !strings.Contains(page.Stdout, `\\ud800`) {
		t.Fatalf("retained surrogate: %+v, %v", page, err)
	}
}
