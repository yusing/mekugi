package outline

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/sourceanalysis/structure"

	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
)

func nativeFixture(t *testing.T, name, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nativeExecute(t *testing.T, args ...string) command.Output {
	t.Helper()
	output, err := (Service{}).Execute(t.Context(), command.Request{Args: args, Stdin: nil})
	if err != nil {
		t.Fatalf("inspect_file %q: %v", args, err)
	}
	return output
}

type nativeOutlineEntry struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Receiver  string `json:"receiver"`
	Line      int    `json:"line"`
	LineEnd   int    `json:"line_end"`
	Pointer   string `json:"pointer"`
	ValueType string `json:"value_type"`
	Level     int    `json:"level"`
}

type nativeInspection struct {
	OK   bool `json:"ok"`
	Data struct {
		Path          string               `json:"path"`
		Kind          string               `json:"kind"`
		Language      *string              `json:"language"`
		SizeBytes     int                  `json:"size_bytes"`
		LineCount     *int                 `json:"line_count"`
		ParseComplete bool                 `json:"parse_complete"`
		Outline       []nativeOutlineEntry `json:"outline"`
	} `json:"data"`
	Truncated  bool `json:"truncated"`
	Truncation *struct {
		Reason       string `json:"reason"`
		AfterEntries int    `json:"after_entries"`
	} `json:"truncation"`
}

func decodeNativeInspection(t *testing.T, stdout string) nativeInspection {
	t.Helper()
	var result nativeInspection
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("inspection missing final LF: %q", stdout)
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid inspection JSON: %v\n%s", err, stdout)
	}
	if !result.OK {
		t.Fatalf("unsuccessful inspection: %s", stdout)
	}
	return result
}

func nativeInspectFixture(t *testing.T, path string) nativeInspection {
	t.Helper()
	out := nativeExecute(t, "--json", path)
	if out.ExitCode != 0 || out.Stderr != "" {
		t.Fatalf("inspection failed: %+v", out)
	}
	return decodeNativeInspection(t, out.Stdout)
}

func nativeTokens(t *testing.T, source string) int {
	t.Helper()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(source)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNativeInspectExactCompactAndJSONSpans(t *testing.T) {
	source := "import {first} from \"one\";\nimport {second} from \"two\";\nfunction visible() {\n  return 1;\n}\n\nclass Box {\n  method() {}\n}"
	path := nativeFixture(t, "sample.ts", source)
	out := nativeExecute(t, path)
	want := "1-2 import\n3-5 function visible\n7-9 class Box\n8-8 method Box.method\n"
	if out.ExitCode != 0 || out.Stdout != want || out.Stderr != "" {
		t.Fatalf("compact: %+v, want %q", out, want)
	}
	result := nativeInspectFixture(t, path)
	wantEntries := []nativeOutlineEntry{
		{Kind: "import", Name: "first", Line: 1, LineEnd: 1}, {Kind: "import", Name: "second", Line: 2, LineEnd: 2},
		{Kind: "function", Name: "visible", Line: 3, LineEnd: 5}, {Kind: "class", Name: "Box", Line: 7, LineEnd: 9},
		{Kind: "method", Name: "method", Receiver: "Box", Line: 8, LineEnd: 8},
	}
	if result.Data.Path != path || result.Data.Kind != "code" || result.Data.Language == nil || *result.Data.Language != "typescript" || result.Data.SizeBytes != len(source) || result.Data.LineCount == nil || *result.Data.LineCount != 9 || !result.Data.ParseComplete || result.Truncated || result.Truncation != nil || !reflect.DeepEqual(result.Data.Outline, wantEntries) {
		t.Fatalf("JSON envelope: %+v", result)
	}
}

// Source: internal/router/toolplugin/tests/tools.test.ts, inspect_file language projections
func TestNativeInspectDeclarationOwnershipAndSpans(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []nativeOutlineEntry
	}{
		{"scope.go", "package p\nvar Exported = func() {\n var localVar = 1\n const localConst = 2\n type localType int\n}\ntype Generic[P any] struct { Field P }\nfunc (g *Generic[P]) Method() {}\n", []nativeOutlineEntry{
			{Kind: "variable", Name: "Exported", Line: 2, LineEnd: 6}, {Kind: "type", Name: "Generic", Line: 7, LineEnd: 7}, {Kind: "method", Name: "Method", Receiver: "*Generic[P]", Line: 8, LineEnd: 8},
		}},
		{"scope.js", "const exported = () => {\n const hidden = 1;\n};\nclass Box {\n field = 'secret';\n method() { function nested() {} }\n}\n", []nativeOutlineEntry{
			{Kind: "constant", Name: "exported", Line: 1, LineEnd: 3}, {Kind: "class", Name: "Box", Line: 4, LineEnd: 7}, {Kind: "method", Name: "method", Receiver: "Box", Line: 6, LineEnd: 6},
		}},
		{"scope.ts", "interface Shape { field: string }\ntype Name = string;\nenum Choice { One }\nabstract class Box {\n abstract transform(\n   value: string,\n ): Promise<string>;\n}\n", []nativeOutlineEntry{
			{Kind: "type", Name: "Shape", Line: 1, LineEnd: 1}, {Kind: "type", Name: "Name", Line: 2, LineEnd: 2}, {Kind: "type", Name: "Choice", Line: 3, LineEnd: 3}, {Kind: "class", Name: "Box", Line: 4, LineEnd: 8}, {Kind: "method", Name: "transform", Receiver: "Box", Line: 5, LineEnd: 7},
		}},
		{"scope.py", "import package.module\nvalue = 1\n@decorate\ndef run():\n    nested = 'secret'\n@decorate\nclass Box:\n    field = 1\n    @decorate\n    def method(self):\n        pass\n", []nativeOutlineEntry{
			{Kind: "import", Name: "package", Line: 1, LineEnd: 1}, {Kind: "variable", Name: "value", Line: 2, LineEnd: 2}, {Kind: "function", Name: "run", Line: 3, LineEnd: 5}, {Kind: "class", Name: "Box", Line: 6, LineEnd: 11}, {Kind: "method", Name: "method", Receiver: "Box", Line: 9, LineEnd: 11},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := nativeInspectFixture(t, nativeFixture(t, tc.name, tc.source))
			if !result.Data.ParseComplete || !reflect.DeepEqual(result.Data.Outline, tc.want) {
				t.Fatalf("outline: %+v, want %+v", result.Data, tc.want)
			}
		})
	}
}

func TestNativeInspectSourceExtensionsAndUnsupportedBytes(t *testing.T) {
	for _, ext := range []string{"ts", "tsx", "mts", "cts", "d.ts", "d.mts", "d.cts", "js", "jsx", "mjs", "cjs", "pyi"} {
		t.Run(ext, func(t *testing.T) {
			source, language := "export const value = 1;\n", "typescript"
			if strings.HasPrefix(ext, "d.") {
				source = "export declare function value(\n input: number,\n): number;\n"
			}
			if ext == "tsx" || ext == "jsx" {
				source = "export const value = <div />;\n"
			}
			if ext == "js" || ext == "jsx" || ext == "mjs" || ext == "cjs" {
				language = "javascript"
			}
			if ext == "pyi" {
				source, language = "value: int\n", "python"
			}
			result := nativeInspectFixture(t, nativeFixture(t, "sample."+ext, source))
			if result.Data.Language == nil || *result.Data.Language != language || !result.Data.ParseComplete || len(result.Data.Outline) != 1 || result.Data.Outline[0].Name != "value" {
				t.Fatalf("format %s: %+v", ext, result.Data)
			}
			if strings.HasPrefix(ext, "d.") && (result.Data.Outline[0].Line != 1 || result.Data.Outline[0].LineEnd != 3) {
				t.Fatalf("ambient declaration span: %+v", result.Data.Outline)
			}
		})
	}
	for _, name := range []string{"blob.bin", "uppercase.GO"} {
		result := nativeInspectFixture(t, nativeFixture(t, name, "\xff\xfe\xfd"))
		if result.Data.Kind != "none" || result.Data.Language != nil || result.Data.LineCount != nil || result.Data.SizeBytes != 3 || !result.Data.ParseComplete || len(result.Data.Outline) != 0 {
			t.Fatalf("unsupported metadata: %+v", result.Data)
		}
	}
}

func TestNativeInspectSideEffectImportDecoding(t *testing.T) {
	source := "import 'single';\nimport \"double\";\nimport 'hex\\x2d\\u0061\\u{1f600}';\nimport 'it\\'s';\nimport 'continued\\\nmodule';\nimport '';\n"
	for _, ext := range []string{"js", "ts"} {
		result := nativeInspectFixture(t, nativeFixture(t, "imports."+ext, source))
		var names []string
		for _, entry := range result.Data.Outline {
			if entry.Kind != "import" {
				t.Fatalf("unexpected entry: %+v", entry)
			}
			names = append(names, entry.Name)
		}
		if !result.Data.ParseComplete || !reflect.DeepEqual(names, []string{"single", "double", "hex-a😀", "it's", "continuedmodule", ""}) {
			t.Fatalf("decoded imports: %+v", result.Data)
		}
	}
}

func TestNativeInspectMarkdownAndJSONProjection(t *testing.T) {
	markdown := "---\r\ntitle: hidden-value\r\nmeta:\r\n  nested: excluded\r\n---\r\n# Main *source* #\r\n```\r\n## hidden\r\n```\r\nSetext\r\n======\r\n### Visible\r\n"
	result := nativeInspectFixture(t, nativeFixture(t, "sample.md", markdown))
	want := []nativeOutlineEntry{{Kind: "frontmatter", Name: "title", Line: 2, LineEnd: 2}, {Kind: "frontmatter", Name: "meta", Line: 3, LineEnd: 3}, {Kind: "heading", Name: "Main *source*", Line: 6, LineEnd: 6, Level: 1}, {Kind: "heading", Name: "Visible", Line: 12, LineEnd: 12, Level: 3}}
	if !reflect.DeepEqual(result.Data.Outline, want) {
		t.Fatalf("markdown: %+v, want %+v", result.Data.Outline, want)
	}
	result = nativeInspectFixture(t, nativeFixture(t, "sample.json", "{\"a/b\":{\"~key\":[true,null,123,\"never-return\"]}}"))
	var pointers []string
	for _, entry := range result.Data.Outline {
		pointers = append(pointers, entry.Pointer+":"+entry.ValueType)
		if entry.Line != 1 || entry.LineEnd != 1 {
			t.Fatalf("JSON span: %+v", entry)
		}
	}
	if !reflect.DeepEqual(pointers, []string{":object", "/a~1b:object", "/a~1b/~0key:array", "/a~1b/~0key/0:boolean", "/a~1b/~0key/1:null", "/a~1b/~0key/2:number", "/a~1b/~0key/3:string"}) {
		t.Fatalf("JSON pointers: %q", pointers)
	}
}

func TestNativeInspectMalformedRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, source, visible string
		errorLine             int
	}{
		{"broken.ts", "const broken = ;\nconst model: typeof import(\"x\") | undefined = undefined;\n", "model", 1},
		{"broken.go", "package p\nfunc Complete() {}\nfunc Malformed( {\n", "Complete", 3},
		{"broken.md", "---\na: 1\na: 2\n---\n", "a", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := nativeInspectFixture(t, nativeFixture(t, tc.name, tc.source))
			visible, diagnostic := false, false
			for _, entry := range result.Data.Outline {
				if entry.Kind == "parse_error" && entry.Name == "syntax error" && entry.Line == tc.errorLine && entry.LineEnd == tc.errorLine {
					diagnostic = true
				}
				if entry.Name == tc.visible {
					visible = true
				}
			}
			if result.Data.ParseComplete || !visible || !diagnostic {
				t.Fatalf("recovery: %+v", result.Data)
			}
		})
	}
	result := nativeInspectFixture(t, nativeFixture(t, "broken.json", "{\"a\" \"x\"}"))
	var values []nativeOutlineEntry
	diagnostic := false
	for _, entry := range result.Data.Outline {
		if entry.Kind == "json" {
			values = append(values, entry)
		}
		if entry.Kind == "parse_error" && entry.Name == "syntax error" && entry.Line == 1 && entry.LineEnd == 1 {
			diagnostic = true
		}
	}
	if result.Data.ParseComplete || len(values) != 1 || values[0].Pointer != "" || !diagnostic {
		t.Fatalf("JSON recovery: %+v", result.Data)
	}
}

func TestNativeInspectMultiTargetErrorsAndSharedBudget(t *testing.T) {
	first := nativeFixture(t, "first.go", "package p\nfunc First() {}\n")
	last := nativeFixture(t, "last.go", "package p\nfunc Last() {}\n")
	missing := filepath.Join(t.TempDir(), "missing.go")
	out := nativeExecute(t, first, missing, last)
	want := fmt.Sprintf("--- %s ---\n2-2 function First\n--- %s ---\n2-2 function Last\n", first, last)
	if out.ExitCode != 1 || out.Stdout != want || !strings.Contains(out.Stderr, missing) {
		t.Fatalf("compact mixed targets: %+v", out)
	}
	out = nativeExecute(t, "--json", first, missing, last)
	rows := strings.Split(strings.TrimSuffix(out.Stdout, "\n"), "\n")
	if out.ExitCode != 1 || len(rows) != 2 {
		t.Fatalf("JSON mixed targets: %+v", out)
	}
	for i, path := range []string{first, last} {
		if decodeNativeInspection(t, rows[i]+"\n").Data.Path != path {
			t.Fatalf("wrong path in %s", rows[i])
		}
	}
	var failure struct {
		OK   bool   `json:"ok"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out.Stderr), &failure); err != nil || failure.OK || failure.Path != missing {
		t.Fatalf("JSON failure envelope: %q (%v)", out.Stderr, err)
	}
	complete := nativeExecute(t, "--json", "--max-tokens", "15500", first, last)
	rows = strings.Split(strings.TrimSuffix(complete.Stdout, "\n"), "\n")
	if complete.ExitCode != 0 || len(rows) != 2 {
		t.Fatalf("complete JSONL: %+v", complete)
	}
	budget := nativeTokens(t, rows[0]+"\n")
	bounded := nativeExecute(t, "--json", "--max-tokens", strconv.Itoa(budget), first, last)
	if bounded.ExitCode != 1 || bounded.Stdout != rows[0]+"\n" || bounded.OmittedOutput == nil || bounded.OmittedOutput.Stdout != rows[1]+"\n" || nativeTokens(t, bounded.Stdout) > budget {
		t.Fatalf("shared JSONL budget: %+v", bounded)
	}
}

func TestNativeInspectSingleJSONBudgetRetainsEntries(t *testing.T) {
	var source strings.Builder
	source.WriteString("package p\n")
	for i := range 100 {
		fmt.Fprintf(&source, "func Function%d() {}\n", i)
	}
	path := nativeFixture(t, "many.go", source.String())
	full := nativeInspectFixture(t, path)
	out := nativeExecute(t, "--json", "--max-tokens", "600", path)
	result := decodeNativeInspection(t, out.Stdout)
	if out.ExitCode != 1 || !result.Truncated || result.Truncation == nil || result.Truncation.Reason != "output_tokens" || result.Truncation.AfterEntries != len(result.Data.Outline) || nativeTokens(t, out.Stdout) > 600 || out.OmittedOutput == nil {
		t.Fatalf("single JSON budget: %+v; result %+v", out, result)
	}
	var omitted []nativeOutlineEntry
	if err := json.Unmarshal([]byte(out.OmittedOutput.Stdout), &omitted); err != nil {
		t.Fatalf("omitted entries not a JSON array: %v", err)
	}
	all := append(result.Data.Outline, omitted...)
	if !reflect.DeepEqual(all, full.Data.Outline) {
		t.Fatalf("retained entries do not reconstruct complete outline")
	}
}

func TestNativeInspectLogicalLineCountsAndDuplicatePointers(t *testing.T) {
	for _, tc := range []struct {
		source string
		count  int
	}{
		{"", 0}, {"a", 1}, {"a\n", 1}, {"a\r\n", 1}, {"a\rb", 2}, {"\n", 1}, {"\n\n", 2},
	} {
		result := nativeInspectFixture(t, nativeFixture(t, "rows.go", tc.source))
		if result.Data.LineCount == nil || *result.Data.LineCount != tc.count {
			t.Fatalf("logical rows for %q: %+v", tc.source, result.Data)
		}
	}
	result := nativeInspectFixture(t, nativeFixture(t, "duplicate.json", "{\n\"a\":1,\n\"a\":2\n}"))
	want := []string{"", "/a", "/a"}
	var pointers []string
	for _, entry := range result.Data.Outline {
		pointers = append(pointers, entry.Pointer)
	}
	if !reflect.DeepEqual(pointers, want) {
		t.Fatalf("duplicate pointers: %q", pointers)
	}
}

func nativeParsedFixture(t *testing.T, name, source string) *structure.Source {
	t.Helper()
	format, ok := sourcekind.Classify(name)
	if !ok {
		t.Fatalf("unsupported fixture %s", name)
	}
	parsed, err := structure.Parse(t.Context(), source, format)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// Source: internal/router/toolplugin/tests/inspect-quality.test.ts
func TestNativeInspectTypeQueryRecoveryAndDeclarationCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name, source, visible, malformed string
		from, to, errorLine              int
	}{
		{"complete.ts", "function complete() {\n return 1;\n}\nfunction malformed( {\n", "complete", "malformed", 1, 3, 4},
		{"suffix.ts", "const broken = ;\nconst model: typeof import(\"x\") | undefined = undefined;\n", "model", "broken", 2, 2, 1},
		{"typequery.ts", "const model: typeof import(\"x\") | undefined = undefined;\nfunction broken( {\n", "model", "broken", 1, 1, 2},
		{"complete.go", "package p\nfunc Complete() { println(1) }\nfunc Malformed( {\n", "Complete", "Malformed", 2, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := nativeParsedFixture(t, tc.name, tc.source)
			found := false
			for _, entry := range parsed.Entries {
				if entry.Name == tc.visible {
					found = true
					if !entry.Complete || entry.Line != tc.from || entry.EndLine != tc.to {
						t.Fatalf("valid declaration range: %+v", entry)
					}
				}
				if entry.Name == tc.malformed && entry.Complete {
					t.Fatalf("malformed declaration marked complete: %+v", entry)
				}
			}
			if !found {
				t.Fatalf("valid declaration %s lost during recovery", tc.visible)
			}
			result := nativeInspectFixture(t, nativeFixture(t, tc.name, tc.source))
			diagnostic := false
			for _, entry := range result.Data.Outline {
				if entry.Kind == "parse_error" {
					if entry.Line >= tc.from && entry.Line <= tc.to {
						t.Fatalf("false diagnostic inside error-free declaration: %+v", entry)
					}
					if entry.Line == tc.errorLine && entry.LineEnd == tc.errorLine {
						diagnostic = true
					}
				}
			}
			if result.Data.ParseComplete || !diagnostic {
				t.Fatalf("missing malformed-source diagnostic: %+v", result.Data)
			}
		})
	}
}

func TestNativeInspectMultilineDeclareMethods(t *testing.T) {
	source := strings.Join([]string{
		"abstract class AbstractThing {", "  abstract transform(", "    value: string,", "    fallback?: number,", "  ): Promise<string>;", "}", "",
		"declare class AmbientThing {", "  dispatch(", "    first: string,", "    second: number,", "  ): Promise<void>;", "}", "",
		"class Overloaded {", "  parse(", "    input: string,", "  ): Result;", "  parse(", "    input: Uint8Array,", "    encoding: string,", "  ): Result;", "  parse(", "    input: string | Uint8Array,", "    encoding?: string,", "  ): Result {", "    return {} as Result;", "  }", "}", "",
	}, "\n")
	want := []nativeOutlineEntry{
		{Kind: "class", Name: "AbstractThing", Line: 1, LineEnd: 6}, {Kind: "method", Name: "transform", Receiver: "AbstractThing", Line: 2, LineEnd: 5},
		{Kind: "class", Name: "AmbientThing", Line: 8, LineEnd: 13}, {Kind: "method", Name: "dispatch", Receiver: "AmbientThing", Line: 9, LineEnd: 12},
		{Kind: "class", Name: "Overloaded", Line: 15, LineEnd: 29}, {Kind: "method", Name: "parse", Receiver: "Overloaded", Line: 16, LineEnd: 18}, {Kind: "method", Name: "parse", Receiver: "Overloaded", Line: 19, LineEnd: 22}, {Kind: "method", Name: "parse", Receiver: "Overloaded", Line: 23, LineEnd: 28},
	}
	result := nativeInspectFixture(t, nativeFixture(t, "declarations.ts", source))
	if !result.Data.ParseComplete || !reflect.DeepEqual(result.Data.Outline, want) {
		t.Fatalf("multiline declare methods: %+v, want %+v", result.Data, want)
	}
	parsed := nativeParsedFixture(t, "declarations.ts", source)
	for _, entry := range parsed.Entries {
		if !entry.Complete {
			t.Fatalf("valid declaration cannot expand: %+v", entry)
		}
	}
}

// Source: internal/router/toolplugin/tests/tools.test.ts, declaration ownership
func TestNativeInspectDeclarationOwnershipCorpus(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		names        []string
	}{
		{"grouped.go", "package p\nvar Exported = func() {\n var localVar = 1\n const localConst = 2\n type localType int\n _ = localVar; _ = localConst; _ = localType(0)\n}\nvar (First, Second = 1, 2; Third = func() { var hidden = 3; _ = hidden })\nconst (Alpha, Beta = 1, 2)\ntype (Generic[P any] struct { Field P }; Alias = int)\n", []string{"Exported", "First", "Second", "Third", "Alpha", "Beta", "Generic", "Alias"}},
		{"methods.js", "import primary, {remote as local} from \"pkg\"; import \"side\";\nexport const callable = () => 1, value = 2; let mutable = 3;\nfunction run() { const hidden = 1; }\nclass Box { field = \"secret\"; method() {} #private() {} 1() {} \"quoted\"() {} [\"literal\"]() {} [name]() {} static async *gen() {} }\n", []string{"primary", "local", "side", "callable", "value", "mutable", "run", "Box", "method", "#private", "1", "\"quoted\"", "[\"literal\"]", "[name]", "gen"}},
		{"assignments.py", "a = b = 1\nobj.attr = 2\nitems[0] = 3\nannotated: Type = 4\nleft, (middle, right) = source_value\n[first_item, second_item] = source_value\n", []string{"a", "b", "annotated", "left", "middle", "right", "first_item", "second_item"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := nativeInspectFixture(t, nativeFixture(t, tc.name, tc.source))
			var names []string
			for _, entry := range result.Data.Outline {
				names = append(names, entry.Name)
			}
			if !result.Data.ParseComplete || !reflect.DeepEqual(names, tc.names) {
				t.Fatalf("declaration-owned names: %+v, want %q", result.Data, tc.names)
			}
		})
	}
}

func TestNativeFrontendInvalidArgumentsAndReadFailures(t *testing.T) {
	path := nativeFixture(t, "valid.go", "package p\n")
	for _, name := range []string{"inspect_file"} {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{{}, {"--max-tokens", "0", path}, {"--max-tokens", "15501", path}, {"--max-tokens", "1", "--max-tokens=2", path}} {
				out := nativeExecute(t, args...)
				if out.ExitCode != 1 || out.Stdout != "" || out.Stderr == "" {
					t.Fatalf("invalid args %q: %+v", args, out)
				}
			}
			for _, target := range []string{filepath.Join(t.TempDir(), "missing \"quoted\".go"), t.TempDir(), nativeFixture(t, "invalid.go", "\xff")} {
				out := nativeExecute(t, target)
				if out.ExitCode != 1 || out.Stderr == "" {
					t.Fatalf("read failure %q: %+v", target, out)
				}
			}
		})
	}
}
