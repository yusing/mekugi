package toolplugin

import (
	"strings"
	"testing"
)

func TestNativeInspectJSONGrammar(t *testing.T) {
	for _, tc := range []struct {
		source   string
		complete bool
		pointer  string
	}{
		{`{"x":1e+2}`, true, "/x"}, {`{"x":-1.25E-2}`, true, "/x"}, {`{"x":1.}`, false, "/x"},
		{`{"x":"\uZZZZ"}`, false, "/x"}, {`{} {}`, false, ""}, {`/* comment */ {"x":1}`, false, "/x"},
		{"// comment\n{\"x\":1}", false, "/x"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			result := nativeInspectFixture(t, nativeFixture(t, "fixture.json", tc.source))
			if result.Data.ParseComplete != tc.complete {
				t.Fatalf("parse_complete: %+v", result.Data)
			}
			found, parseError := false, false
			for _, entry := range result.Data.Outline {
				if entry.Kind == "json" && entry.Pointer == tc.pointer {
					found = true
				}
				if entry.Kind == "parse_error" {
					parseError = true
				}
			}
			if !found || parseError == tc.complete {
				t.Fatalf("recognized values/diagnostics: %+v", result.Data.Outline)
			}
		})
	}
}

func TestNativeInspectNestedMarkdownATX(t *testing.T) {
	source := "> # Quoted heading\n\n- ## List heading\n\n> - ### Nested *heading* ###\n\nSetext\n------\n\n```md\n# hidden\n```\n"
	result := nativeExecute(t, "inspect_file", nativeFixture(t, "fixture.md", source))
	want := "1-1 heading Quoted heading\n3-3 heading List heading\n5-5 heading Nested *heading*\n"
	if result.ExitCode != 0 || result.Stdout != want {
		t.Fatalf("headings: %+v, want %q", result, want)
	}
}

func TestNativeInspectConstKeywordWhitespace(t *testing.T) {
	for _, extension := range []string{"js", "ts"} {
		for _, separator := range []string{"\n", "\t", "/*comment*/ "} {
			source := "const" + separator + "value = 1;\n"
			result := nativeExecute(t, "inspect_file", nativeFixture(t, "fixture."+extension, source))
			if result.ExitCode != 0 || !strings.Contains(result.Stdout, " constant value\n") {
				t.Fatalf("const separator %q: %+v", separator, result)
			}
		}
	}
}

func TestNativeMCatTrailingOptionTerminator(t *testing.T) {
	file := nativeFixture(t, "fixture.txt", "read\n")
	result := nativeExecute(t, "mcat", file, "--")
	if result.ExitCode != 0 || result.Stdout != "read\n" {
		t.Fatalf("trailing terminator: %+v", result)
	}
}

func TestNativeInspectMultipleJSONRootDiagnosticPosition(t *testing.T) {
	result := nativeInspectFixture(t, nativeFixture(t, "roots.json", "null\n{\n}\n"))
	found := false
	for _, entry := range result.Data.Outline {
		if entry.Kind == "parse_error" {
			found = true
			if entry.Line != 2 || entry.LineEnd != 2 {
				t.Fatalf("second-root diagnostic: %+v", entry)
			}
		}
	}
	if result.Data.ParseComplete || !found {
		t.Fatalf("missing second-root diagnostic: %+v", result.Data)
	}
}

func TestNativeSymbolRejectsNonFileLocationURIs(t *testing.T) {
	for _, uri := range []string{"relative.ts", "git:/document.ts", "file:///source.ts?revision=1"} {
		raw := nativeJSON(map[string]any{"uri": uri, "range": symbolRange{Start: symbolPosition{0, 0}, End: symbolPosition{0, 1}}})
		locations, err := parseNativeLocations([]byte(raw), "def")
		if err != nil || len(locations) != 1 || !locations[0].outside {
			t.Fatalf("non-file URI %q: %+v, %v", uri, locations, err)
		}
	}
}
