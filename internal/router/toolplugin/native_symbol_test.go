package toolplugin

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type symbolFixturePosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type symbolFixtureLocation struct {
	URI   string `json:"uri"`
	Range struct {
		Start symbolFixturePosition `json:"start"`
		End   symbolFixturePosition `json:"end"`
	} `json:"range"`
}
type symbolResolverFixture struct {
	Definitions   []symbolFixtureLocation
	References    []symbolFixtureLocation
	CLIOutput     string
	MutateMethod  string
	MutatePath    string
	MutatedSource string
	ErrorMethod   string
}
type symbolProtocolEvent struct {
	Event   string   `json:"event"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	CWD     string   `json:"cwd"`
	Message struct {
		ID     jsontext.Value `json:"id"`
		Method string         `json:"method"`
		Params struct {
			Position symbolFixturePosition `json:"position"`
		} `json:"params"`
	} `json:"message"`
}

func nativeSymbolFixtureLocation(path string, line, first, last int) symbolFixtureLocation {
	var location symbolFixtureLocation
	location.URI = (&url.URL{Scheme: "file", Path: path}).String()
	location.Range.Start = symbolFixturePosition{line, first}
	location.Range.End = symbolFixturePosition{line, last}
	return location
}

func writeSymbolFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func installSymbolResolvers(t *testing.T, fixtures map[string]symbolResolverFixture) string {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	for command, fixture := range fixtures {
		writeSymbolFixture(t, filepath.Join(directory, command+".json"), &fixture)
		script := "#!/bin/sh\nexec " + quote(executable) + " -test.run='^TestNativeSymbolResolverProcess$' -- " + quote(command) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(directory, command), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MEKUGI_NATIVE_SYMBOL_FIXTURE", directory)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(directory, "protocol.jsonl")
}

// TestNativeSymbolResolverProcess is a short-lived fake resolver launched by the
// production process owner. It implements CLI responses and framed LSP replies,
// without depending on an installed Go/TypeScript/Python language server.
func TestNativeSymbolResolverProcess(t *testing.T) {
	directory := os.Getenv("MEKUGI_NATIVE_SYMBOL_FIXTURE")
	if directory == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	command := args[0]
	data, err := os.ReadFile(filepath.Join(directory, command+".json"))
	if err != nil {
		os.Exit(2)
	}
	var fixture symbolResolverFixture
	if json.Unmarshal(data, &fixture) != nil {
		os.Exit(2)
	}
	log, err := os.OpenFile(filepath.Join(directory, "protocol.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	appendLog := func(value any) {
		body, err := json.Marshal(value)
		if err != nil {
			os.Exit(2)
		}
		if _, err := log.Write(append(body, '\n')); err != nil {
			os.Exit(2)
		}
	}
	cwd, _ := os.Getwd()
	appendLog(map[string]any{"event": "start", "command": command, "args": args[1:], "cwd": cwd})
	if command == "gopls" && (len(args) < 2 || args[1] != "serve") {
		if fixture.MutateMethod == "cli" {
			if os.WriteFile(fixture.MutatePath, []byte(fixture.MutatedSource), 0o600) != nil {
				os.Exit(2)
			}
		}
		fmt.Print(fixture.CLIOutput)
		os.Exit(0)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		length := -1
		for {
			header, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			if header == "\r\n" || header == "\n" {
				break
			}
			if value, ok := strings.CutPrefix(strings.TrimSpace(header), "Content-Length:"); ok {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if length < 0 || length > 1<<20 {
			os.Exit(2)
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			os.Exit(2)
		}
		var event symbolProtocolEvent
		if json.Unmarshal(body, &event.Message) != nil {
			os.Exit(2)
		}
		event.Event, event.Command = "message", command
		appendLog(&event)
		method := event.Message.Method
		if method == "exit" {
			os.Exit(0)
		}
		if method == fixture.MutateMethod {
			if os.WriteFile(fixture.MutatePath, []byte(fixture.MutatedSource), 0o600) != nil {
				os.Exit(2)
			}
		}
		if len(event.Message.ID) == 0 {
			continue
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{"positionEncoding": "utf-16"}}
		case "textDocument/definition":
			result = fixture.Definitions
		case "textDocument/references":
			result = fixture.References
		case "shutdown":
			result = nil
		default:
			result = nil
		}
		response := map[string]any{"jsonrpc": "2.0", "id": event.Message.ID, "result": result}
		if method == fixture.ErrorMethod {
			delete(response, "result")
			response["error"] = map[string]any{"code": -32001, "message": "fixture query rejected"}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("Content-Length: %d\r\n\r\n%s", len(encoded), encoded)
	}
}

func readSymbolLog(t *testing.T, path string) []symbolProtocolEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []symbolProtocolEvent
	for row := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if row == "" {
			continue
		}
		var event symbolProtocolEvent
		if err := json.Unmarshal([]byte(row), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func writeSymbolSource(t *testing.T, name, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(path))
	return path
}

// Source: internal/router/toolplugin/tests/symbol-batch.test.ts, Go outline lookup
func TestNativeSymbolGoCLIByteOffsetsAndExactExpansion(t *testing.T) {
	source := "\ufeffpackage p\n// 😀 名稱\nfunc Pick() {\n println(1)\n}\n"
	path := writeSymbolSource(t, "sample.go", source)
	start := strings.Index(source, "Pick")
	uri := nativeSymbolFixtureLocation(path, 2, 5, 9).URI
	span := map[string]any{"span": map[string]any{"uri": uri, "start": map[string]int{"line": 3, "column": 6, "offset": start}, "end": map[string]int{"line": 3, "column": 10, "offset": start + 4}}}
	encoded, err := json.Marshal(&span)
	if err != nil {
		t.Fatal(err)
	}
	log := installSymbolResolvers(t, map[string]symbolResolverFixture{"gopls": {CLIOutput: string(encoded) + "\n"}})
	out := nativeExecute(t, "msymbol", "def", "sample.go", "pkg.Pick")
	want := "\"sample.go\":3-5\nfunc Pick() {\n println(1)\n}\n"
	if out.ExitCode != 0 || out.Stdout != want || out.Stderr != "" {
		t.Fatalf("definition expansion: %+v", out)
	}
	events := readSymbolLog(t, log)
	if len(events) != 1 || !reflect.DeepEqual(events[0].Args, []string{"definition", "-json", path + ":#" + strconv.Itoa(start)}) || events[0].CWD != filepath.Dir(path) {
		t.Fatalf("byte-offset CLI invocation: %+v", events)
	}
}

func TestNativeSymbolSelectionAndConfinementBeforeStartup(t *testing.T) {
	source := "package p\nfunc Use() {\n 名稱 := 1; _ = 名稱; _ = \"名稱\" // 名稱\n}\n"
	path := writeSymbolSource(t, "path with spaces.go", source)
	log := installSymbolResolvers(t, map[string]symbolResolverFixture{"gopls": {}})
	outside := nativeFixture(t, "external.go", source)
	if err := os.Symlink(outside, "escaped.go"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"refs", "path with spaces.go", "3", "名稱"}, {"refs", "path with spaces.go", "3", "名稱", "3"},
		{"refs", "path with spaces.go", "3", "名稱", "01"}, {"refs", "path with spaces.go", "3", "func"},
		{"refs", "path with spaces.go", "3", "Name"}, {"refs", "path with spaces.go", "名稱"},
		{"refs", outside, "3", "名稱", "1"}, {"refs", "escaped.go", "3", "名稱", "1"},
	} {
		out := nativeExecute(t, "msymbol", args...)
		if out.ExitCode != 1 || out.Stdout != "" || out.Stderr == "" {
			t.Fatalf("invalid selector %q: %+v", args, out)
		}
	}
	if events := readSymbolLog(t, log); len(events) != 0 {
		t.Fatalf("resolver started for invalid selection: %+v", events)
	}
	out := nativeExecute(t, "msymbol", "refs", "\"path with spaces.go\":3", "名稱", "2")
	if out.ExitCode != 0 || out.Stdout != "" || out.Stderr != "" {
		t.Fatalf("second true token: %+v", out)
	}
	first := strings.Index(source, "名稱")
	second := first + len("名稱") + strings.Index(source[first+len("名稱"):], "名稱")
	events := readSymbolLog(t, log)
	if len(events) != 1 || !reflect.DeepEqual(events[0].Args, []string{"references", "-d", path + ":#" + strconv.Itoa(second)}) {
		t.Fatalf("exact occurrence byte offset: %+v", events)
	}
}

func TestNativeSymbolOneLSPPerLanguageAndTupleOrder(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	goSource := "package p\nfunc Pick() {\n println(1)\n}\nfunc Use() { Pick() }\n"
	tsSource := "export function Pick() {\n return 1;\n}\nPick();\n"
	goPath, tsPath := filepath.Join(root, "sample.go"), filepath.Join(root, "sample.ts")
	for path, source := range map[string]string{goPath: goSource, tsPath: tsSource} {
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := installSymbolResolvers(t, map[string]symbolResolverFixture{
		"gopls": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(goPath, 1, 5, 9)}, References: []symbolFixtureLocation{nativeSymbolFixtureLocation(goPath, 1, 5, 9), nativeSymbolFixtureLocation(goPath, 4, 13, 17), nativeSymbolFixtureLocation(goPath, 4, 13, 17)}},
		"tsc":   {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(tsPath, 0, 16, 20)}, References: []symbolFixtureLocation{nativeSymbolFixtureLocation(tsPath, 0, 16, 20), nativeSymbolFixtureLocation(tsPath, 3, 0, 4)}},
	})
	out := nativeExecute(t, "msymbol", "def", "sample.go:2", "Pick", "def", "sample.ts:1", "Pick", "refs", "sample.go:5", "Pick", "refs", "sample.ts:4", "Pick")
	want := "\"sample.go\":2-4\nfunc Pick() {\n println(1)\n}\n\"sample.ts\":1-3\nexport function Pick() {\n return 1;\n}\n\"sample.go\":\n2 func Pick() {\n5 func Use() { Pick() }\n\"sample.ts\":\n1 export function Pick() {\n4 Pick();\n"
	if out.ExitCode != 0 || out.Stdout != want || out.Stderr != "" {
		t.Fatalf("ordered mixed-language batch: %+v", out)
	}
	starts := map[string]int{}
	methods := map[string]int{}
	for _, event := range readSymbolLog(t, log) {
		if event.Event == "start" {
			starts[event.Command]++
			expected := []string{"serve"}
			if event.Command == "tsc" {
				expected = []string{"--lsp", "--stdio"}
			}
			if !reflect.DeepEqual(event.Args, expected) || event.CWD != root {
				t.Fatalf("resolver launch: %+v", event)
			}
		}
		if event.Event == "message" {
			methods[event.Command+":"+event.Message.Method]++
		}
	}
	for _, command := range []string{"gopls", "tsc"} {
		if starts[command] != 1 {
			t.Fatalf("server counts: %v", starts)
		}
		for _, method := range []string{"initialize", "textDocument/definition", "textDocument/references"} {
			if methods[command+":"+method] != 1 {
				t.Fatalf("protocol counts: %v", methods)
			}
		}
	}
}

func TestNativeSymbolUTF16AndUnrelatedSyntaxError(t *testing.T) {
	source := "export function Pick() {\n return 1;\n}\nconst icon = '😀'; Pick();\nfunction broken( {\n"
	path := writeSymbolSource(t, "sample.ts", source)
	log := installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}}})
	out := nativeExecute(t, "msymbol", "def", "sample.ts", "4", "Pick")
	want := "\"sample.ts\":1-3\nexport function Pick() {\n return 1;\n}\n"
	if out.ExitCode != 0 || out.Stdout != want {
		t.Fatalf("error-free declaration expansion: %+v", out)
	}
	seen := false
	for _, event := range readSymbolLog(t, log) {
		if event.Message.Method == "textDocument/definition" {
			seen = true
			if event.Message.Params.Position != (symbolFixturePosition{3, 19}) {
				t.Fatalf("UTF-16 position: %+v", event.Message.Params.Position)
			}
		}
	}
	if !seen {
		t.Fatal("missing definition request")
	}
}

func TestNativeSymbolMixedInvalidTuplesAndProtocolErrors(t *testing.T) {
	source := "export function Pick() {}\n"
	path := writeSymbolSource(t, "sample.ts", source)
	installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}, References: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}}})
	out := nativeExecute(t, "msymbol", "def", "sample.ts", "1", "Pick", "refs", "sample.ts", "99", "Pick", "def", "missing.ts", "1", "Pick", "refs", "sample.ts", "1", "Pick")
	want := "\"sample.ts\":1-1\n" + source + "\"sample.ts\":\n1 " + source
	if out.ExitCode != 1 || out.Stdout != want || !strings.Contains(out.Stderr, "line 99 is past EOF") || !strings.Contains(out.Stderr, "missing.ts") {
		t.Fatalf("mixed valid/invalid tuples: %+v", out)
	}
	installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {ErrorMethod: "textDocument/definition", References: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}}})
	out = nativeExecute(t, "msymbol", "def", "sample.ts", "1", "Pick", "refs", "sample.ts", "1", "Pick")
	if out.ExitCode != 1 || out.Stdout != "\"sample.ts\":\n1 "+source || !strings.Contains(out.Stderr, "fixture query rejected") {
		t.Fatalf("protocol failure suppressed later tuple: %+v", out)
	}
}

func TestNativeSymbolChangedInputPreservesIndependentTuple(t *testing.T) {
	source := "export function Pick() {}\nPick();\n"
	path := writeSymbolSource(t, "sample.ts", source)
	other := filepath.Join(filepath.Dir(path), "other.ts")
	otherSource := "export function Other() {}\n"
	if err := os.WriteFile(other, []byte(otherSource), 0o600); err != nil {
		t.Fatal(err)
	}
	installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(other, 0, 16, 21)}, MutateMethod: "textDocument/definition", MutatePath: path, MutatedSource: source + "Pick();\n"}})
	out := nativeExecute(t, "msymbol", "def", "sample.ts", "1", "Pick", "def", "other.ts", "1", "Other")
	if out.ExitCode != 1 || out.Stdout != "\"other.ts\":1-1\n"+otherSource || !strings.Contains(out.Stderr, "input changed during query") {
		t.Fatalf("changed input isolation: %+v", out)
	}
}

func TestNativeSymbolDefinitionCanonicalWorkspaceConfinement(t *testing.T) {
	source := "export function Pick() {}\n"
	path := writeSymbolSource(t, "sample.ts", source)
	outside := nativeFixture(t, "external.ts", source)
	alias := filepath.Join(filepath.Dir(path), "escape.ts")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(alias, 0, 16, 20)}, References: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}}})
	out := nativeExecute(t, "msymbol", "def", "sample.ts", "1", "Pick", "refs", "sample.ts", "1", "Pick")
	if out.ExitCode != 1 || out.Stdout != "\"sample.ts\":\n1 "+source || !strings.Contains(out.Stderr, "definition has no editable workspace location") {
		t.Fatalf("escaped result confinement: %+v", out)
	}
	installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}}})
	workspace := filepath.Dir(path)
	caller := t.TempDir()
	t.Chdir(caller)
	out = nativeExecute(t, "msymbol", "--workspace", workspace, "def", "sample.ts", "Pick")
	want := fmt.Sprintf("%q:1-1\n%s", path, source)
	if out.ExitCode != 0 || out.Stdout != want {
		t.Fatalf("explicit workspace result: %+v, want %q", out, want)
	}
	if cwd, err := os.Getwd(); err != nil || cwd != caller {
		t.Fatalf("resolver changed caller cwd: %q, %v", cwd, err)
	}
}

func TestNativeSymbolSharedBudgetRetainsCompleteRows(t *testing.T) {
	source := "export function Pick() {\n return 1;\n}\nPick();\n"
	path := writeSymbolSource(t, "sample.ts", source)
	installSymbolResolvers(t, map[string]symbolResolverFixture{"tsc": {Definitions: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20)}, References: []symbolFixtureLocation{nativeSymbolFixtureLocation(path, 0, 16, 20), nativeSymbolFixtureLocation(path, 3, 0, 4)}}})
	definition := "\"sample.ts\":1-3\nexport function Pick() {\n return 1;\n}\n"
	references := "\"sample.ts\":\n1 export function Pick() {\n4 Pick();\n"
	budget := nativeTokens(t, definition)
	out := nativeExecute(t, "msymbol", "--max-tokens", strconv.Itoa(budget), "def", "sample.ts", "1", "Pick", "refs", "sample.ts", "4", "Pick")
	if out.ExitCode != 1 || out.Stdout != definition || out.OmittedOutput == nil || out.OmittedOutput.Stdout != references || out.OmittedOutput.StdoutKind != "rows" || nativeTokens(t, out.Stdout) > budget {
		t.Fatalf("shared output budget: %+v", out)
	}
}
