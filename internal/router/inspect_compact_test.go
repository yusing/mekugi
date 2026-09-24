package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inspectCompactInvocation(directory string) shellWorkerTestInvocation {
	invocation := newShellWorkerTestInvocation(directory)
	environment := invocation.environment[:0]
	for _, entry := range invocation.environment {
		if !strings.HasPrefix(entry, "BASH_ENV=") {
			environment = append(environment, entry)
		}
	}
	invocation.environment = environment
	return invocation
}

func inspectCommand(paths []string, maxTokens int, jsonOutput bool) string {
	var command strings.Builder
	command.WriteString("inspect_file --max-tokens ")
	command.WriteString(fmt.Sprint(maxTokens))
	if jsonOutput {
		command.WriteString(" --json")
	}
	for _, source := range paths {
		command.WriteByte(' ')
		command.WriteString(shellQuoteArgument(source))
	}
	return command.String()
}

func inspectFixture(t *testing.T, directory, name string, functions int) string {
	t.Helper()
	var source strings.Builder
	source.WriteString("package p\n")
	for index := range functions {
		fmt.Fprintf(&source, "func Item%02d() {}\n", index)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func inspectContinuationReference(t *testing.T, diagnostic string) string {
	t.Helper()
	_, continuation, found := strings.Cut(diagnostic, "next_call: mread ")
	if !found {
		t.Fatalf("missing inspect_file continuation: %q", diagnostic)
	}
	fields := strings.Fields(continuation)
	if len(fields) == 0 {
		t.Fatalf("empty inspect_file continuation: %q", diagnostic)
	}
	return fields[0]
}

type inspectRecovery struct {
	full      string
	initial   string
	pages     []string
	recovered string
}

func recoverInspectAfterRemovingSources(
	t *testing.T,
	registry *toolRegistry,
	directory string,
	paths []string,
	budget int,
	jsonOutput bool,
	recoveryBudget int,
) inspectRecovery {
	t.Helper()
	invocation := inspectCompactInvocation(directory)
	full, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		inspectCommand(paths, 15_500, jsonOutput), nil, invocation)
	if status != 0 || diagnostic != "" {
		t.Fatalf("full inspect_file read: status=%d stderr=%q", status, diagnostic)
	}
	initial, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		inspectCommand(paths, budget, jsonOutput), nil, invocation)
	if status != 1 {
		t.Fatalf("bounded inspect_file read did not truncate: status=%d stdout=%q stderr=%q", status, initial, diagnostic)
	}
	reference := inspectContinuationReference(t, diagnostic)
	for _, source := range paths {
		if err := os.Remove(source); err != nil {
			t.Fatalf("remove source before recovery (%s): %v", source, err)
		}
	}

	recovered := initial
	var pages []string
	for pageIndex := range 100 {
		page, nextDiagnostic, pageStatus := runShellWorkerTest(t, registry, "bash", nil,
			fmt.Sprintf("mread %s --max-tokens %d", reference, recoveryBudget), nil, invocation)
		if page == "" {
			t.Fatalf("empty inspect_file recovery page %d: status=%d stderr=%q", pageIndex, pageStatus, nextDiagnostic)
		}
		pages = append(pages, page)
		recovered += page
		if pageStatus == 0 {
			return inspectRecovery{
				full: full, initial: initial, pages: pages, recovered: recovered,
			}
		}
		reference = inspectContinuationReference(t, nextDiagnostic)
	}
	t.Fatal("inspect_file recovery did not complete")
	return inspectRecovery{}
}

func TestInspectFileFrontendExactCompactJSONAndMultiPathOutput(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "fixture.ts")
	source := "import {first} from \"one\";\n" +
		"import {second} from \"two\";\n" +
		"function visible() {\n  return 1;\n}\n\n" +
		"class Box {\n  method() {}\n}\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := inspectCompactInvocation(directory)
	compact, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		inspectCommand([]string{path}, 15_500, false), nil, invocation)
	wantCompact := "1-2 import\n3-5 function visible\n7-9 class Box\n8-8 method Box.method\n"
	if status != 0 || diagnostic != "" || compact != wantCompact {
		t.Fatalf("compact output: status=%d stdout=%q stderr=%q want=%q", status, compact, diagnostic, wantCompact)
	}

	jsonOutput, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		inspectCommand([]string{path}, 15_500, true), nil, invocation)
	quotedPath, err := json.Marshal(filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := fmt.Sprintf(`{"ok":true,"data":{"path":%s,"kind":"code","language":"typescript","size_bytes":%d,"line_count":9,"parse_complete":true,"outline":[{"kind":"import","name":"first","line":1,"line_end":1},{"kind":"import","name":"second","line":2,"line_end":2},{"kind":"function","name":"visible","line":3,"line_end":5},{"kind":"class","name":"Box","line":7,"line_end":9},{"kind":"method","name":"method","receiver":"Box","line":8,"line_end":8}]},"truncated":false,"truncation":null}`+"\n", quotedPath, len(source))
	if status != 0 || diagnostic != "" || jsonOutput != wantJSON {
		t.Fatalf("JSON output: status=%d stdout=%q stderr=%q want=%q", status, jsonOutput, diagnostic, wantJSON)
	}

	first := filepath.Join(directory, "first.go")
	second := filepath.Join(directory, "second.go")
	for file, declaration := range map[string]string{first: "First", second: "Second"} {
		if err := os.WriteFile(file, []byte("package p\nfunc "+declaration+"() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{first, second}
	multiple, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		inspectCommand(paths, 15_500, false), nil, invocation)
	wantMultiple := fmt.Sprintf("--- %s ---\n2-2 function First\n--- %s ---\n2-2 function Second\n",
		filepath.ToSlash(first), filepath.ToSlash(second))
	if status != 0 || diagnostic != "" || multiple != wantMultiple {
		t.Fatalf("multi-path compact output: status=%d stdout=%q stderr=%q want=%q", status, multiple, diagnostic, wantMultiple)
	}

	jsonl, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		inspectCommand(paths, 15_500, true), nil, invocation)
	firstJSON, err := json.Marshal(filepath.ToSlash(first))
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(filepath.ToSlash(second))
	if err != nil {
		t.Fatal(err)
	}
	wantJSONL := fmt.Sprintf("{\"ok\":true,\"data\":{\"path\":%s,\"kind\":\"code\",\"language\":\"go\",\"size_bytes\":%d,\"line_count\":2,\"parse_complete\":true,\"outline\":[{\"kind\":\"function\",\"name\":\"First\",\"line\":2,\"line_end\":2}]},\"truncated\":false,\"truncation\":null}\n"+
		"{\"ok\":true,\"data\":{\"path\":%s,\"kind\":\"code\",\"language\":\"go\",\"size_bytes\":%d,\"line_count\":2,\"parse_complete\":true,\"outline\":[{\"kind\":\"function\",\"name\":\"Second\",\"line\":2,\"line_end\":2}]},\"truncated\":false,\"truncation\":null}\n",
		firstJSON, len("package p\nfunc First() {}\n"), secondJSON, len("package p\nfunc Second() {}\n"))
	if status != 0 || diagnostic != "" || jsonl != wantJSONL {
		t.Fatalf("multi-path JSONL output: status=%d stdout=%q stderr=%q want=%q", status, jsonl, diagnostic, wantJSONL)
	}
}

func TestInspectFileCompactRecoveryAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)

	t.Run("multi-path labels survive recovery", func(t *testing.T) {
		directory := t.TempDir()
		first := inspectFixture(t, directory, "first.go", 12)
		second := inspectFixture(t, directory, "second.go", 12)
		paths := []string{first, second}
		result := recoverInspectAfterRemovingSources(t, registry, directory, paths, 64, false, 64)
		if result.recovered != result.full {
			t.Fatalf("multi-path compact recovery changed output:\nrecovered %q\nfull      %q", result.recovered, result.full)
		}
		if len(result.pages) < 2 {
			t.Fatalf("fixture did not exercise multi-page recovery: pages=%d", len(result.pages))
		}
		firstHeader, secondHeader := "--- "+filepath.ToSlash(first)+" ---\n", "--- "+filepath.ToSlash(second)+" ---\n"
		if !strings.Contains(result.recovered, firstHeader) || !strings.Contains(result.recovered, secondHeader) {
			t.Fatalf("recovery lost a path header: %q", result.recovered)
		}
		if strings.Contains(result.initial, secondHeader) || !strings.Contains(strings.Join(result.pages, ""), secondHeader) {
			t.Fatalf("second path header was not preserved in continuation pages: initial=%q pages=%q", result.initial, result.pages)
		}
	})
}

func TestInspectFileJSONLMultiPathByteRecoveryAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	first := inspectFixture(t, directory, "first.go", 8)
	second := inspectFixture(t, directory, "second.go", 8)
	result := recoverInspectAfterRemovingSources(t, registry, directory, []string{first, second}, 64, true, 192)
	if result.recovered != result.full {
		t.Fatalf("multi-path JSONL byte recovery changed output:\nrecovered %q\nfull      %q", result.recovered, result.full)
	}
	if len(result.pages) < 2 || strings.Contains(result.pages[0], "\n") {
		t.Fatalf("fixture did not exercise a JSONL document split across byte pages: pages=%q", result.pages)
	}
	for _, source := range []string{first, second} {
		if !strings.Contains(result.recovered, `"path":"`+filepath.ToSlash(source)+`"`) {
			t.Errorf("recovered JSONL lost path label %q", source)
		}
	}
}
