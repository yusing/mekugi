package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	codexinstructions "github.com/yusing/mekugi/contrib/codex"
	"github.com/yusing/mekugi/internal/shellruntime"
)

const (
	testTranslatedPatch = "*** Begin Patch\n*** Add File: created.txt\n+payload\n*** End Patch\n"
	testMekugiScript    = "new created.txt\ntype \"payload\"\n"
	testMekugiReport    = "in created.txt\nlast type created.txt 1 ranges 1:1-1:1\nfiles add=1 update=0 move=0 delete=0\nrefs 2 type created.txt\n1:239f payload\n"
)

const testMekugiToolDescription = "fixture mekugi description\nwith exact trailing newline\n"

const testCodeModeDescription = "Run JavaScript.\n- All nested tools are available on the global `tools` object, for example `await tools.exec_command(...)`. Tool names are exposed as normalized JavaScript identifiers.\n\n### `exec_command`\nRun a shell command.\n\nexec tool declaration:\n```ts\ndeclare const tools: { exec_command(args: { cmd: string; workdir?: string }): Promise<unknown>; };\n```\n\n### `apply_patch`\nThe default editor.\n\nexec tool declaration:\n```ts\ndeclare const tools: { apply_patch(input: string): Promise<unknown>; };\n```\n\n### `create_goal`\nCreate a goal."

const testCLICodeModeDescription = "Run JavaScript.\n- All nested tools are available on the global `tools` object, for example `await tools.exec_command(...)`. Tool names are exposed as normalized JavaScript identifiers.\n\n### exec_command\nRun a command in a PTY.\n\nParameters:\n- `cmd`: required command text.\n- `workdir`: optional working directory.\n- `tty`: optional terminal allocation.\n\n### `apply_patch`\nThe default editor.\n\nexec tool declaration:\n```ts\ndeclare const tools: { apply_patch(input: string): Promise<unknown>; };\n```\n\n### `create_goal`\nCreate a goal."

func testCodeModeAdditionalTools(description string) map[string]any {
	return map[string]any{
		"type":   "additional_tools",
		"role":   "developer",
		"future": map[string]any{"kept": true},
		"tools": []any{
			map[string]any{
				"type":        "namespace",
				"name":        "functions",
				"description": "",
				"future":      true,
				"tools": []any{
					map[string]any{
						"type":        "custom",
						"name":        "exec",
						"description": description,
						"format":      map[string]any{"type": "text"},
						"future":      true,
					},
					map[string]any{"type": "function", "name": "wait", "future": true},
				},
			},
			map[string]any{
				"type":   "namespace",
				"name":   "collaboration",
				"future": true,
				"tools":  []any{map[string]any{"type": "function", "name": "send_message", "future": true}},
			},
		},
	}
}

func testFlatCodeModeAdditionalTools(description string) map[string]any {
	return map[string]any{
		"type":   "additional_tools",
		"role":   "developer",
		"future": map[string]any{"kept": true},
		"tools": []any{
			map[string]any{
				"type":        "custom",
				"name":        "exec",
				"description": description,
				"format":      map[string]any{"type": "text"},
				"future":      true,
			},
			map[string]any{"type": "function", "name": "wait", "future": true},
			map[string]any{
				"type":   "namespace",
				"name":   "collaboration",
				"future": true,
				"tools":  []any{map[string]any{"type": "function", "name": "send_message", "future": true}},
			},
		},
	}
}

func testFunctionsNamespaceTools(t *testing.T, fields map[string]json.RawMessage) ([]map[string]json.RawMessage, []map[string]json.RawMessage) {
	t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || jsonString(items[0], "type") != "additional_tools" {
		t.Fatalf("input has no leading additional_tools item: %#v", items)
	}
	var namespaces []map[string]json.RawMessage
	if err := json.Unmarshal(items[0]["tools"], &namespaces); err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(namespaces, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "type") == "namespace" && jsonString(tool, "name") == "functions"
	})
	if index < 0 {
		t.Fatalf("additional_tools has no functions namespace: %#v", namespaces)
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(namespaces[index]["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	return tools, namespaces
}

func testInstalledTools() []*responsesToolDefinition {
	return []*responsesToolDefinition{
		newResponsesToolDefinition(customGrammarTool(mekugiToolName, testMekugiToolDescription, mekugi.ToolGrammar())),
		newResponsesToolDefinition(map[string]json.RawMessage{
			"type":        mustMarshalJSON("custom"),
			"name":        mustMarshalJSON("shell"),
			"description": mustMarshalJSON("shell base description"),
		}),
	}
}

func testNativeResponsesTools() []any {
	return []any{
		map[string]any{"type": "function", "name": nativeExecCommandToolName, "description": "run a command"},
		map[string]any{"type": "custom", "name": applyPatchToolName, "description": "apply a patch"},
		map[string]any{"type": "function", "name": "lookup", "future": true},
	}
}

type mekugiTranslatorFunc func(context.Context, string, string) ([]byte, error)

func (f mekugiTranslatorFunc) Translate(ctx context.Context, workspace string, script string) (mekugiTranslationResult, error) {
	patch, err := f(ctx, workspace, script)
	return mekugiTranslationResult{patch: patch, report: testMekugiReport}, err
}

func (mekugiTranslatorFunc) ToolDescription() string {
	return testMekugiToolDescription
}

type mekugiResultTranslatorFunc func(context.Context, string, string) (mekugiTranslationResult, error)

func (f mekugiResultTranslatorFunc) Translate(ctx context.Context, workspace string, script string) (mekugiTranslationResult, error) {
	return f(ctx, workspace, script)
}

func (mekugiResultTranslatorFunc) ToolDescription() string {
	return testMekugiToolDescription
}

func newManagedMekugiProxy(t *testing.T, translator mekugiTranslator) *mekugiProxy {
	t.Helper()
	if translator == nil {
		return nil
	}
	if translator.ToolDescription() != testMekugiToolDescription {
		return newManagedMekugiProxyWithDataDirectory(t, translator, t.TempDir())
	}
	return newProxyWithSharedTestRegistry(t, translator, sharedProxyTestRegistry(t))
}

func newManagedMekugiProxyWithDataDirectory(t *testing.T, translator mekugiTranslator, dataDirectory string) *mekugiProxy {
	t.Helper()
	if translator == nil {
		return nil
	}
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, t.TempDir())
	registry, err := buildToolRegistry(t.Context(), dataDirectory, translator.ToolDescription(), false)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newMekugiProxy(translator, registry, false, false)
	t.Cleanup(func() {
		if err := errors.Join(proxy.Close(), registry.Close()); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

func registeredWorkerInput(t *testing.T, proxy *mekugiProxy, name string, arguments []string) string {
	t.Helper()
	contribution, ok := proxy.registry.contribution(name)
	if !ok {
		t.Fatalf("registered worker %q is unavailable", name)
	}
	input, err := proxy.registry.execCarrierPayload(codeModeCarrierCustom, contribution, "", arguments, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func newMekugiTestTransform(t *testing.T, translator mekugiTranslator) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest, string) {
	t.Helper()
	return newMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t, translator))
}

func newMekugiTestTransformWithProxy(t *testing.T, proxy *mekugiProxy) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest, string) {
	t.Helper()
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test",
		"input": []any{
			testCodeModeAdditionalTools(testCodeModeDescription),
			map[string]any{"role": "user", "content": "task", "future": true},
		},
		"tools":               []any{map[string]any{"type": "function", "name": "lookup", "future": true}},
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
		"future_request":      map[string]any{"kept": true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("prepareRequest returned no transform")
	}
	t.Cleanup(func() {
		transform.Close()
	})
	return transform, proxy, &request, workspace
}

func newNativeMekugiTestTransformWithProxy(t *testing.T, proxy *mekugiProxy) (*mekugiResponseTransform, *parsedResponsesRequest) {
	t.Helper()
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model":       "gpt-test",
		"input":       []any{map[string]any{"role": "user", "content": "task"}},
		"tools":       testNativeResponsesTools(),
		"tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(
		t.Context(),
		&request,
		"native-session",
		"native-thread",
		codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, &request
}

func testTranslator(t *testing.T, calls *int) mekugiTranslator {
	t.Helper()
	return mekugiTranslatorFunc(func(_ context.Context, directory string, script string) ([]byte, error) {
		*calls++
		if directory == "" {
			t.Fatal("translator received no base directory")
		}
		if script != testMekugiScript {
			t.Fatalf("script = %q", script)
		}
		return []byte(testTranslatedPatch), nil
	})
}

func testMekugiItem() map[string]any {
	return map[string]any{
		"type":    "custom_tool_call",
		"id":      "item-H",
		"call_id": "call-H",
		"name":    mekugiToolName,
		"input":   testMekugiScript,
		"status":  "completed",
		"future":  map[string]any{"kept": true},
	}
}

func TestBuildCodeModeCarrierCatalogReadsNamespacedTools(t *testing.T) {
	additional := testCodeModeAdditionalTools(testCodeModeDescription)
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{additional}),
	}
	registry := &toolRegistry{byName: map[string]toolContribution{}}
	catalog, err := buildCodeModeCarrierCatalog(decodeResponsesToolCatalog(fields), registry)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog["exec"]; got != codeModeCarrierCustom {
		t.Fatalf("exec carrier kind = %q", got)
	}

	namespaces := additional["tools"].([]any)
	functionsNamespace := namespaces[0].(map[string]any)
	nestedTools := functionsNamespace["tools"].([]any)
	nestedTools[0].(map[string]any)["type"] = "function"
	fields["input"] = mustTestJSON(t, []any{additional})
	catalog, err = buildCodeModeCarrierCatalog(decodeResponsesToolCatalog(fields), registry)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog["exec"]; got != codeModeCarrierFunction {
		t.Fatalf("exec carrier kind = %q", got)
	}
}

func TestBuildCodeModeCarrierCatalogReadsFlatTools(t *testing.T) {
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{testFlatCodeModeAdditionalTools(testCodeModeDescription)}),
	}
	registry := &toolRegistry{byName: map[string]toolContribution{}}
	catalog, err := buildCodeModeCarrierCatalog(decodeResponsesToolCatalog(fields), registry)
	if err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]codeModeCarrierKind{
		"exec":         codeModeCarrierCustom,
		"wait":         codeModeCarrierFunction,
		"send_message": codeModeCarrierFunction,
	} {
		if got := catalog[name]; got != kind {
			t.Fatalf("%s carrier kind = %q, want %q", name, got, kind)
		}
	}
}

func TestBuildCodeModeCarrierCatalogRejectsDuplicateNames(t *testing.T) {
	additional := testCodeModeAdditionalTools(testCodeModeDescription)
	namespaces := additional["tools"].([]any)
	functionsNamespace := namespaces[0].(map[string]any)
	functionsNamespace["tools"] = append(functionsNamespace["tools"].([]any), map[string]any{
		"type": "custom", "name": "exec", "description": "duplicate",
	})
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{additional}),
	}
	registry := &toolRegistry{byName: map[string]toolContribution{}}
	if _, err := buildCodeModeCarrierCatalog(decodeResponsesToolCatalog(fields), registry); err == nil || !strings.Contains(err.Error(), "defined more than once") {
		t.Fatalf("duplicate carrier error = %v", err)
	}
}

func TestMekugiPrepareRequestRewritesNamespacedExecWithShell(t *testing.T) {
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"input":        []any{testCodeModeAdditionalTools(testCodeModeDescription)},
		"tools":        []any{map[string]any{"type": "function", "name": "lookup", "future": true}},
		"tool_choice":  "auto",
		"instructions": stockModelInstructionsForTest("existing base\n", "existing suffix\n"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))

	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session-functions-exec", "thread-functions-exec", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("prepareRequest returned no transform")
	}
	defer transform.Close()

	functionsTools, namespaces := testFunctionsNamespaceTools(t, request.fields)
	execIndex := slices.IndexFunc(functionsTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "exec"
	})
	if execIndex < 0 {
		t.Fatalf("functions namespace lost exec: %#v", functionsTools)
	}
	description := jsonString(functionsTools[execIndex], "description")
	for _, forbidden := range []string{codeModeApplyPatchHeading, codeModeExecCommandHeading, "tools.apply_patch", "tools.exec_command", "exec_command(args:"} {
		if strings.Contains(description, forbidden) {
			t.Fatalf("namespaced exec description contains %q: %q", forbidden, description)
		}
	}
	if !strings.Contains(description, "### `create_goal`") {
		t.Fatalf("namespaced exec lost unrelated nested tool: %q", description)
	}
	if !slices.ContainsFunc(functionsTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "wait" && string(tool["future"]) == "true"
	}) {
		t.Fatalf("functions namespace lost sibling tools: %#v", functionsTools)
	}
	if !slices.ContainsFunc(namespaces, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "collaboration" && string(tool["future"]) == "true"
	}) {
		t.Fatalf("additional_tools lost sibling namespace: %#v", namespaces)
	}
	var rewrittenInstructions string
	if err := json.Unmarshal(request.fields["instructions"], &rewrittenInstructions); err != nil {
		t.Fatal(err)
	}
	wantInstructions := "existing base\n" + codexinstructions.InstructionsForModel("", false) + "existing suffix\n"
	if rewrittenInstructions != wantInstructions {
		t.Fatalf("request instructions = %q, want %q", rewrittenInstructions, wantInstructions)
	}
}

func TestMekugiPrepareRequestSupportsAstraStockInstructions(t *testing.T) {
	stock, err := os.ReadFile("testdata/gpt-6-astra-instructions.txt")
	if err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model":        "gpt-6-astra",
		"input":        []any{testCodeModeAdditionalTools(testCodeModeDescription)},
		"tool_choice":  "auto",
		"instructions": string(stock),
	}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{t.TempDir(): nil}}
	transform, err := proxy.prepareRequest(t.Context(), &request, "astra-session", "astra-thread", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("Astra request did not receive HPATCH tool projection")
	}
	defer transform.Close()
	if request.model() != "gpt-6-astra" {
		t.Fatalf("Astra model was changed to %q", request.model())
	}
	var instructions string
	if err := json.Unmarshal(request.fields["instructions"], &instructions); err != nil {
		t.Fatal(err)
	}
	if strings.Count(instructions, codexinstructions.InstructionsForModel("gpt-6-astra", false)) != 1 ||
		strings.Contains(instructions, stockRGInstruction) || strings.Contains(instructions, stockExecInstruction) {
		t.Fatal("Astra request did not receive exactly one replacement guidance section")
	}
}

func TestMekugiPrepareRequestRefreshesWorkflowOnModelSwitch(t *testing.T) {
	for _, compact := range []bool{false, true} {
		for _, developer := range []bool{false, true} {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			proxy.compactModelProtocol = compact
			metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{t.TempDir(): nil}}
			instructions := "prefix\n" + codexinstructions.InstructionsForModel("", false) + "suffix\n"
			for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-6-astra-2026-09-01"} {
				input := []any{testCodeModeAdditionalTools(testCodeModeDescription)}
				fields := map[string]any{"model": model, "tool_choice": "auto", "instructions": instructions}
				if developer {
					delete(fields, "instructions")
					input = append([]any{map[string]any{"type": "message", "role": "developer", "content": instructions}}, input...)
				}
				fields["input"] = input
				request, err := parseResponsesRequest(mustTestJSON(t, fields))
				if err != nil {
					t.Fatal(err)
				}
				transform, err := proxy.prepareRequest(t.Context(), &request, "model-switch-session", "model-switch-thread", metadata, true)
				if err != nil {
					t.Fatal(err)
				}
				transform.Close()
				if developer {
					var rewritten []map[string]any
					if err := json.Unmarshal(request.fields["input"], &rewritten); err != nil {
						t.Fatal(err)
					}
					instructions = rewritten[0]["content"].(string)
				} else if err := json.Unmarshal(request.fields["instructions"], &instructions); err != nil {
					t.Fatal(err)
				}
				want := "prefix\n" + codexinstructions.InstructionsForModel(model, compact) + "suffix\n"
				if instructions != want || request.model() != model {
					t.Fatalf("model %q compact %v developer %v: incorrect request-local refresh", model, compact, developer)
				}
			}
		}
	}
}

func TestMekugiPrepareRequestUsesCustomizedModelInstructions(t *testing.T) {
	workspace := t.TempDir()
	newRequest := func(t *testing.T) parsedResponsesRequest {
		t.Helper()
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
			"input":        []any{testCodeModeAdditionalTools(testCodeModeDescription)},
			"tool_choice":  "auto",
			"instructions": "custom instructions\n",
		}))
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}

	t.Run("configured file appends", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
		proxy.customizedInstructions = true
		request := newRequest(t)
		transform, err := proxy.prepareRequest(t.Context(), &request, "custom-session", "custom-thread", metadata, true)
		if err != nil {
			t.Fatal(err)
		}
		defer transform.Close()
		var instructions string
		if err := json.Unmarshal(request.fields["instructions"], &instructions); err != nil {
			t.Fatal(err)
		}
		want := "custom instructions\n\n" + codexinstructions.InstructionsForModel("", false)
		if instructions != want {
			t.Fatalf("instructions = %q, want %q", instructions, want)
		}
	})

	t.Run("unconfigured prompt fails before tool rewrite", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
		request := newRequest(t)
		originalInput := bytes.Clone(request.fields["input"])
		if _, err := proxy.prepareRequest(t.Context(), &request, "stock-session", "stock-thread", metadata, true); err == nil ||
			!strings.Contains(err.Error(), "neither stock nor marked") {
			t.Fatalf("error = %v", err)
		}
		if !bytes.Equal(request.fields["input"], originalInput) {
			t.Fatal("failed instruction rewrite changed Code Mode tools")
		}
	})
}

func TestMekugiPrepareRequestExposesEditToolsAndShell(t *testing.T) {
	transform, _, request, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	if !transform.originalToolsPresent || len(transform.originalTools) == 0 {
		t.Fatal("original top-level tools were not retained")
	}
	var topTools []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["tools"], &topTools); err != nil {
		t.Fatal(err)
	}
	if len(topTools) != 5 || jsonString(topTools[4], "name") != journalToolName || jsonString(topTools[0], "name") != "lookup" || jsonString(topTools[1], "name") != mekugiToolName || jsonString(topTools[2], "name") != mekugiRecoveryToolName || jsonString(topTools[3], "name") != "shell" {
		t.Fatalf("top-level tools = %#v", topTools)
	}
	if jsonString(topTools[1], "type") != "custom" {
		t.Fatalf("standalone mekugi definition = %#v", topTools[1])
	}
	var format struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	if err := json.Unmarshal(topTools[1]["format"], &format); err != nil {
		t.Fatal(err)
	}
	if format.Type != "grammar" || format.Syntax != "lark" || format.Definition != mekugi.ToolGrammar() {
		t.Fatalf("standalone mekugi format = %#v", topTools[1])
	}
	exposed := jsonString(topTools[1], "description")
	if exposed != testMekugiToolDescription {
		t.Fatalf("standalone mekugi description = %q, want native tool help only", exposed)
	}
	if description := jsonString(topTools[3], "description"); !strings.HasPrefix(description, "Run free-form scripts") || !strings.Contains(description, "\n\n### `#!params`") ||
		strings.Contains(description, "#!cmd=") || strings.Contains(description, "@shell/") {
		t.Fatalf("standalone shell description = %q", description)
	}
	if _, exists := request.fields["instructions"]; exists {
		t.Fatalf("prepareRequest added instructions: %s", request.fields["instructions"])
	}
	if string(topTools[0]["future"]) != "true" {
		t.Fatalf("unrelated top-level tool changed: %#v", topTools[0])
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || jsonString(items[0], "type") != "additional_tools" || string(items[1]["future"]) != "true" {
		t.Fatalf("rewritten input items = %#v", items)
	}
	functionsTools, namespaces := testFunctionsNamespaceTools(t, request.fields)
	if len(namespaces) != 2 || string(namespaces[0]["future"]) != "true" || jsonString(namespaces[1], "name") != "collaboration" {
		t.Fatalf("additional tool namespaces changed unexpectedly: %#v", namespaces)
	}
	execIndex := slices.IndexFunc(functionsTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "exec"
	})
	if execIndex < 0 || string(functionsTools[execIndex]["future"]) != "true" {
		t.Fatalf("functions namespace changed unexpectedly: %#v", functionsTools)
	}
	description := jsonString(functionsTools[execIndex], "description")
	if strings.Contains(description, codeModeApplyPatchHeading) ||
		strings.Contains(description, "tools.apply_patch") ||
		strings.Contains(description, codeModeExecCommandHeading) ||
		strings.Contains(description, "tools.exec_command") ||
		!strings.Contains(description, "### `create_goal`") {
		t.Fatalf("native apply_patch or exec_command was not hidden: %q", description)
	}
	if !bytes.Contains(items[0]["future"], []byte(`"kept":true`)) || !bytes.Contains(request.fields["future_request"], []byte(`"kept":true`)) {
		t.Fatalf("future fields were not preserved: %#v", request.fields)
	}
	if string(request.fields["parallel_tool_calls"]) != "true" {
		t.Fatalf("parallel_tool_calls = %s", request.fields["parallel_tool_calls"])
	}
}

func TestMekugiNativeToolsUseExecCommandCarrierAndRestoreOriginalContract(t *testing.T) {
	workspace := t.TempDir()
	originalTools := mustTestJSON(t, testNativeResponsesTools())
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model":               "gpt-test",
		"input":               []any{map[string]any{"role": "user", "content": "task"}},
		"tools":               json.RawMessage(originalTools),
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, err := proxy.prepareRequest(
		t.Context(),
		&request,
		"native-session",
		"native-thread",
		codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	if !transform.nativeTools || transform.codeModeToolName != nativeExecCommandToolName {
		t.Fatalf("native carrier = %q, native %t", transform.codeModeToolName, transform.nativeTools)
	}
	var rewrittenTools []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["tools"], &rewrittenTools); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{nativeExecCommandToolName, "lookup", mekugiToolName, mekugiRecoveryToolName, "shell"} {
		if !slices.ContainsFunc(rewrittenTools, func(tool map[string]json.RawMessage) bool {
			return jsonString(tool, "name") == name
		}) {
			t.Fatalf("rewritten native tools lost %q: %#v", name, rewrittenTools)
		}
	}
	if slices.ContainsFunc(rewrittenTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == applyPatchToolName
	}) {
		t.Fatalf("rewritten native tools retained apply_patch: %#v", rewrittenTools)
	}

	originalItem := mustTestJSON(t, testMekugiItem())
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status":      "completed",
		"output":      []any{json.RawMessage(originalItem)},
		"tools":       rewrittenTools,
		"tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
		Tools  json.RawMessage              `json:"tools"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response.Tools, originalTools) || len(response.Output) != 1 {
		t.Fatalf("restored native response contract = %s", visible)
	}
	carrier := response.Output[0]
	if jsonString(carrier, "type") != "function_call" || jsonString(carrier, "name") != nativeExecCommandToolName {
		t.Fatalf("native translated carrier = %s", mustTestJSON(t, carrier))
	}
	var arguments struct {
		Command string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(jsonString(carrier, "arguments")), &arguments); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(arguments.Command, mekugiNativeApplyMarker) ||
		!strings.Contains(arguments.Command, shellQuoteArgument(testTranslatedPatch)) ||
		!strings.Contains(arguments.Command, shellQuoteArgument(testMekugiReport)) {
		t.Fatalf("native exec_command arguments = %q", arguments.Command)
	}

	replay, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{
		carrier,
		map[string]any{"type": "function_call_output", "call_id": "call-H", "output": testMekugiReport},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var replayed []json.RawMessage
	if err := json.Unmarshal(replay.fields["input"], &replayed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed[0], originalItem) {
		t.Fatalf("restored native replay = %s, want %s", replayed[0], originalItem)
	}
	var replayedOutput map[string]json.RawMessage
	if err := json.Unmarshal(replayed[1], &replayedOutput); err != nil {
		t.Fatal(err)
	}
	if jsonString(replayedOutput, "type") != "custom_tool_call_output" ||
		jsonString(replayedOutput, "output") != testMekugiReport {
		t.Fatalf("restored native replay output = %s", replayed[1])
	}

	outputOnly, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{
		map[string]any{"type": "function_call_output", "call_id": "call-H", "output": testMekugiReport},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&outputOnly, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var restoredOutputOnly []map[string]json.RawMessage
	if err := json.Unmarshal(outputOnly.fields["input"], &restoredOutputOnly); err != nil {
		t.Fatal(err)
	}
	if len(restoredOutputOnly) != 1 || jsonString(restoredOutputOnly[0], "type") != "custom_tool_call_output" ||
		jsonString(restoredOutputOnly[0], "output") != testMekugiReport {
		t.Fatalf("restored output-only native replay = %#v", restoredOutputOnly)
	}
}

func TestMekugiNativeExecCommandAppliesPatchAndReturnsOnlyReport(t *testing.T) {
	var arguments struct {
		Command string `json:"cmd"`
		Login   *bool  `json:"login"`
	}
	nativeInput := renderExecCarrier(
		codeModeCarrierFunction,
		execCommandArguments(mekugiNativeCommand(mekugiHistory{patch: testTranslatedPatch, report: testMekugiReport}), nil),
		false,
		nil,
	)
	if err := json.Unmarshal([]byte(nativeInput), &arguments); err != nil {
		t.Fatal(err)
	}
	if arguments.Login == nil || *arguments.Login {
		t.Fatalf("native carrier login = %v, want false", arguments.Login)
	}
	bin := t.TempDir()
	capturedPatch := filepath.Join(t.TempDir(), "patch")
	applyPatch := filepath.Join(bin, applyPatchToolName)
	if err := os.WriteFile(applyPatch, []byte("#!/bin/sh\ncat >\"$MEKUGI_CAPTURE\"\nprintf 'native apply output\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "bash", "-c", arguments.Command)
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "MEKUGI_CAPTURE="+capturedPatch)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != testMekugiReport {
		t.Fatalf("native carrier output = %q, want report only", output)
	}
	patch, err := os.ReadFile(capturedPatch)
	if err != nil {
		t.Fatal(err)
	}
	if string(patch) != testTranslatedPatch {
		t.Fatalf("native carrier patch = %q", patch)
	}
}

func TestMekugiNativeExecCommandPreservesFailureOutput(t *testing.T) {
	var arguments struct {
		Command string `json:"cmd"`
	}
	nativeInput := renderExecCarrier(
		codeModeCarrierFunction,
		execCommandArguments(mekugiNativeCommand(mekugiHistory{patch: testTranslatedPatch, report: testMekugiReport}), nil),
		false,
		nil,
	)
	if err := json.Unmarshal([]byte(nativeInput), &arguments); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	applyPatch := filepath.Join(bin, applyPatchToolName)
	want := "native apply failure\n\n"
	if err := os.WriteFile(applyPatch, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'native apply failure\\n\\n'\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "bash", "-c", arguments.Command)
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.Output()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 7 {
		t.Fatalf("native carrier error = %v", err)
	}
	if string(output) != want {
		t.Fatalf("native carrier failure output = %q, want %q", output, want)
	}
}

func TestMekugiNativeDiagnosticAndAlreadySatisfiedUseReportCarriers(t *testing.T) {
	tests := []struct {
		name       string
		translated mekugiTranslationResult
		err        error
		marker     string
	}{
		{
			name:       "diagnostic",
			translated: mekugiTranslationResult{diagnostic: "type: command 2, reason row-stale: current row\n"},
			err:        errors.New("rejected"),
			marker:     mekugiNativeDiagnosticMarker,
		},
		{
			name: "already satisfied",
			translated: mekugiTranslationResult{
				report: "in file.txt\nlast none\n",
				change: mekugi.HostChange{AlreadySatisfied: true},
			},
			marker: mekugiNativeReportMarker,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translator := mekugiResultTranslatorFunc(func(context.Context, string, string) (mekugiTranslationResult, error) {
				return test.translated, test.err
			})
			transform, _ := newNativeMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t, translator))
			history, err := transform.translate("call-H", testMekugiScript, nil)
			if err != nil {
				t.Fatal(err)
			}
			if history.effectiveCarrierKind() != codeModeCarrierFunction || history.carrierName != nativeExecCommandToolName {
				t.Fatalf("native result carrier = %+v", history)
			}
			var arguments struct {
				Command string `json:"cmd"`
			}
			if err := json.Unmarshal([]byte(history.carrierInput()), &arguments); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(arguments.Command, test.marker) {
				t.Fatalf("native result command = %q", arguments.Command)
			}
		})
	}
}

func TestMekugiNativeToolsTranslateShellAndStreamingMekugi(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _ := newNativeMekugiTestTransformWithProxy(t, proxy)
	history, err := transform.translateTool("shell", "call-shell", "printf ok\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if history.effectiveCarrierKind() != codeModeCarrierFunction || history.carrierName != nativeExecCommandToolName {
		t.Fatalf("native shell carrier = %+v", history)
	}
	var arguments struct {
		Command string `json:"cmd"`
		Login   *bool  `json:"login"`
	}
	if err := json.Unmarshal([]byte(history.carrierInput()), &arguments); err != nil {
		t.Fatal(err)
	}
	if arguments.Login == nil || *arguments.Login ||
		!strings.Contains(arguments.Command, "shell bash") || !strings.Contains(arguments.Command, `"retained"`) {
		t.Fatalf("native shell arguments = %q", arguments.Command)
	}

	stream, _ := newNativeMekugiTestTransformWithProxy(t, proxy)
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""
	if visible, err := stream.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil || visible != nil {
		t.Fatalf("native buffered added = %q, error %v", visible, err)
	}
	done := mustTestJSON(t, map[string]any{
		"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testMekugiScript,
	})
	visible, err := stream.TransformSSE(done)
	if err != nil || len(visible) != 2 || !bytes.Contains(visible[0], []byte(`"type":"function_call"`)) ||
		!bytes.Contains(visible[1], []byte(`"type":"response.function_call_arguments.done"`)) {
		t.Fatalf("native streaming carrier = %q, error %v", visible, err)
	}
}

func TestMekugiRoutesOnlyModelVisibleRegistryTools(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	for name, want := range map[string]bool{
		mekugiToolName: true,
		"shell":        true,
		"hcat":         false,
		"hgrep":        false,
		"hsymbol":      false,
		"inspect_file": false,
		"lookup":       false,
	} {
		if got := transform.routesTool(name); got != want {
			t.Errorf("routesTool(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestReportIssueRouting(t *testing.T) {
	dataDirectory := t.TempDir()
	registry, err := buildToolRegistry(t.Context(), dataDirectory, testMekugiToolDescription, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})

	t.Run("runs current router hook without worker", func(t *testing.T) {
		bodyPath := filepath.Join(t.TempDir(), "body.md")
		settings := fmt.Sprintf(
			`{"hooks":{"diagnose":[%q]}}`,
			"printf '%s\n%s' {{shellquote .Title}} {{shellquote (format_markdown .)}} > "+bodyPath,
		)
		if err := os.WriteFile(filepath.Join(dataDirectory, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}

		indexPath := filepath.Join(t.TempDir(), "session_index.jsonl")
		if err := os.WriteFile(indexPath, []byte(`{"id":"session-1","thread_name":"Fix loop flaws"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		proxy := newMekugiProxy(
			testTranslator(t, new(int)),
			registry,
			false,
			false,
			newSessionTitleCacheAt(indexPath),
		)
		t.Cleanup(func() {
			if err := proxy.Close(); err != nil {
				t.Error(err)
			}
		})

		updatedBodyPath := filepath.Join(t.TempDir(), "updated-body.md")
		updatedSettings := fmt.Sprintf(
			`{"hooks":{"diagnose":[%q]}}`,
			"printf '%s\n%s' {{shellquote .Title}} {{shellquote (format_markdown .)}} > "+updatedBodyPath,
		)
		if err := os.WriteFile(filepath.Join(dataDirectory, "settings.json"), []byte(updatedSettings), 0o600); err != nil {
			t.Fatal(err)
		}
		transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)

		markdown := "# mekugi issue\n\nRepair context did not identify the stale row."
		history, err := transform.translateTool(reportIssueToolName, "call-report", markdown, nil)
		if err != nil {
			t.Fatal(err)
		}
		if history.toolName != reportIssueToolName ||
			history.carrierName != transform.codeModeToolName ||
			history.report != "Issue reported." ||
			history.carrierInput() != "text("+strconv.Quote("Issue reported.")+");" {
			t.Fatalf("report issue history = %+v", history)
		}

		body, err := os.ReadFile(updatedBodyPath)
		if err != nil {
			t.Fatal(err)
		}
		want := "Fix loop flaws\n" + markdown
		if string(body) != want {
			t.Fatalf("diagnose hook output = %q, want %q", body, want)
		}
		if _, ok := registry.wrapper(reportIssueToolName); ok {
			t.Fatal("report issue unexpectedly installed a worker wrapper")
		}
	})

	t.Run("hook failure does not fail routing", func(t *testing.T) {
		if err := os.WriteFile(
			filepath.Join(dataDirectory, "settings.json"),
			[]byte(`{"hooks":{"diagnose":["exit 9"]}}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		proxy := newMekugiProxy(testTranslator(t, new(int)), registry, false, false)
		t.Cleanup(func() {
			if err := proxy.Close(); err != nil {
				t.Error(err)
			}
		})
		transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)

		history, err := transform.translateTool(reportIssueToolName, "call-report", "diagnostic", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "Issue report was not delivered.\nmekugi: warning: running diagnose hook 1: exit status 9\n"
		if history.report != want || history.carrierInput() != "text("+strconv.Quote(want)+");" {
			t.Fatalf("report issue history = %+v, want report %q", history, want)
		}
	})

	t.Run("call ID cannot be reused by mekugi", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dataDirectory, "settings.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		calls := 0
		proxy := newMekugiProxy(testTranslator(t, &calls), registry, false, false)
		t.Cleanup(func() {
			if err := proxy.Close(); err != nil {
				t.Error(err)
			}
		})
		transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)

		input := "same input"
		if _, err := transform.translateTool(reportIssueToolName, "shared-call", input, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := transform.translateTool(mekugiToolName, "shared-call", input, nil); err == nil ||
			!strings.Contains(err.Error(), "mekugi call \"shared-call\" changed input") {
			t.Fatalf("reused cross-tool call error = %v", err)
		}
		if calls != 0 {
			t.Fatalf("mekugi translations = %d, want 0", calls)
		}
	})
}

func TestMekugiReplacementReplacesNamespacedExecCommandWithShellParams(t *testing.T) {
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{testCodeModeAdditionalTools(testCLICodeModeDescription)}),
		"tools": mustTestJSON(t, []any{}),
	}
	installed := testInstalledTools()
	owner, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), installed)
	if err != nil || !replaced || owner != "exec" {
		t.Fatalf("owner = %q, replaced %v, error %v", owner, replaced, err)
	}
	functionsTools, namespaces := testFunctionsNamespaceTools(t, fields)
	if len(namespaces) != 2 || jsonString(namespaces[1], "name") != "collaboration" {
		t.Fatalf("sibling namespace changed: %#v", namespaces)
	}
	execIndex := slices.IndexFunc(functionsTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "exec"
	})
	if execIndex < 0 {
		t.Fatalf("functions namespace lost exec: %#v", functionsTools)
	}
	description := jsonString(functionsTools[execIndex], "description")
	for _, forbidden := range []string{
		codeModeApplyPatchHeading,
		codeModeExecCommandHeading,
		codeModeExecCommandPlainHeading,
		"tools.exec_command",
		"exec_command(args:",
		"declare const tools: { exec_command",
	} {
		if strings.Contains(description, forbidden) {
			t.Fatalf("exec description contains %q: %q", forbidden, description)
		}
	}
	if !strings.Contains(description, "### `create_goal`") {
		t.Fatalf("rewritten exec description lost unrelated tools: %q", description)
	}

	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	shellIndex := slices.IndexFunc(tools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "shell"
	})
	wantShellDescription := "shell base description\n\n### `#!params`\nThe leading `#!params={...}` directive accepts a JSON object with these request-specific fields. The script body supplies `cmd`, so omit it.\n\n- `workdir`: optional working directory.\n- `tty`: optional terminal allocation."
	if shellIndex < 0 || jsonString(tools[shellIndex], "description") != wantShellDescription {
		t.Fatalf("shell description = %#v, want %q", tools, wantShellDescription)
	}
}

func TestMekugiReplacementReplacesFlatExecCommandWithShellParams(t *testing.T) {
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{testFlatCodeModeAdditionalTools(testCodeModeDescription)}),
		"tools": mustTestJSON(t, []any{}),
	}
	installed := testInstalledTools()
	owner, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), installed)
	if err != nil || !replaced || owner != "exec" {
		t.Fatalf("owner = %q, replaced %v, error %v", owner, replaced, err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !bytes.Contains(items[0]["future"], []byte(`"kept":true`)) {
		t.Fatalf("flat additional_tools item changed: %#v", items)
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(items[0]["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	execIndex := slices.IndexFunc(tools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "type") == "custom" && jsonString(tool, "name") == "exec"
	})
	if execIndex < 0 {
		t.Fatalf("flat additional_tools lost exec: %#v", tools)
	}
	description := jsonString(tools[execIndex], "description")
	for _, forbidden := range []string{codeModeApplyPatchHeading, codeModeExecCommandHeading, "tools.apply_patch", "tools.exec_command"} {
		if strings.Contains(description, forbidden) {
			t.Fatalf("flat exec description contains %q: %q", forbidden, description)
		}
	}
	if !strings.Contains(description, "### `create_goal`") ||
		!slices.ContainsFunc(tools, func(tool map[string]json.RawMessage) bool {
			return jsonString(tool, "name") == "wait" && string(tool["future"]) == "true"
		}) ||
		!slices.ContainsFunc(tools, func(tool map[string]json.RawMessage) bool {
			return jsonString(tool, "name") == "collaboration" && string(tool["future"]) == "true"
		}) {
		t.Fatalf("flat exec rewrite lost sibling content: %#v", tools)
	}

	var installedTools []map[string]json.RawMessage
	if err := json.Unmarshal(fields["tools"], &installedTools); err != nil {
		t.Fatal(err)
	}
	shellIndex := slices.IndexFunc(installedTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "shell"
	})
	wantShellDescription := "shell base description\n\n### `#!params`\nThe leading `#!params={...}` directive accepts this request-specific JSON object shape. The script body supplies `cmd`, so omit it.\n\n```ts\n{ workdir?: string }\n```"
	if shellIndex < 0 || jsonString(installedTools[shellIndex], "description") != wantShellDescription {
		t.Fatalf("shell description = %#v, want %q", installedTools, wantShellDescription)
	}
}

func TestMekugiReplacementKeepsBaseShellDescriptionWithoutExecCommandContract(t *testing.T) {
	description := "Run JavaScript.\n\n### `apply_patch`\nThe default editor.\n\nexec tool declaration:\n```ts\ndeclare const tools: { apply_patch(input: string): Promise<unknown>; };\n```\n\n### `create_goal`\nCreate a goal."
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{testCodeModeAdditionalTools(description)}),
		"tools": mustTestJSON(t, []any{}),
	}
	installed := testInstalledTools()

	_, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), installed)
	if err != nil || !replaced {
		t.Fatalf("replacement = %t, error %v", replaced, err)
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	shellIndex := slices.IndexFunc(tools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "shell"
	})
	if shellIndex < 0 || jsonString(tools[shellIndex], "description") != "shell base description" {
		t.Fatalf("shell description = %#v", tools)
	}
}

func TestMekugiReplacementRejectsUnsupportedAndDuplicateExecCarriers(t *testing.T) {
	flat := func(name string) []any {
		return []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type": "custom", "name": name, "description": testCodeModeDescription,
			}},
		}}
	}
	tests := []struct {
		name  string
		input []any
		tools []any
	}{
		{name: "flat functions exec", input: flat("functions.exec")},
		{
			name:  "top-level and additional exec",
			input: []any{testCodeModeAdditionalTools(testCodeModeDescription)},
			tools: []any{map[string]any{"type": "custom", "name": "exec", "description": testCodeModeDescription}},
		},
		{
			name:  "top-level functions exec",
			input: []any{testCodeModeAdditionalTools(testCodeModeDescription)},
			tools: []any{map[string]any{"type": "custom", "name": "functions.exec", "description": testCodeModeDescription}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]json.RawMessage{
				"input": mustTestJSON(t, test.input),
				"tools": mustTestJSON(t, test.tools),
			}
			beforeInput := bytes.Clone(fields["input"])
			beforeTools := bytes.Clone(fields["tools"])
			_, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), testInstalledTools())
			if err == nil || replaced {
				t.Fatalf("replacement = %t, error %v", replaced, err)
			}
			if !bytes.Equal(fields["input"], beforeInput) || !bytes.Equal(fields["tools"], beforeTools) {
				t.Fatalf("rejected request mutated: %#v", fields)
			}
		})
	}
}

func TestStripCodeModeExecCommandSectionAcceptsAppAndCLISchemas(t *testing.T) {
	descriptions := map[string]string{
		"app": "Run JavaScript.\n\n### `exec_command` \t\nRun a command.\n\nexec tool declaration:\n```ts\ndeclare const tools: { exec_command(args: { cmd: string; workdir?: string; tty?: boolean }): Promise<{ output: string }>; };\n```\n\n### `create_goal`\nKeep this.",
		"cli": "Run JavaScript.\n\n### exec_command\t \nRun a command in a PTY.\n\nParameters:\n- `cmd`: required command text.\n- `workdir`: optional working directory.\n- `yield_time_ms`: optional initial wait.\n\n### `create_goal`\nKeep this.",
	}
	for name, baseDescription := range descriptions {
		for lineEndingName, lineEnding := range map[string]string{"lf": "\n", "crlf": "\r\n"} {
			t.Run(name+"/"+lineEndingName, func(t *testing.T) {
				description := strings.ReplaceAll(baseDescription, "\n", lineEnding)
				stripped, section, found, err := stripCodeModeExecCommandSection(description)
				if err != nil || !found {
					t.Fatalf("found = %t, error %v", found, err)
				}
				want := strings.ReplaceAll("Run JavaScript.\n\n### `create_goal`\nKeep this.", "\n", lineEnding)
				if !strings.Contains(section, "exec_command") || stripped != want {
					t.Fatalf("section = %q, stripped = %q, want %q", section, stripped, want)
				}
			})
		}
	}
}

func TestStripCodeModeExecCommandContractRejectsUnownedReference(t *testing.T) {
	description := "Run JavaScript with `tools.exec_command(...)`.\n\n### `create_goal`\nKeep this."
	if _, _, _, err := stripCodeModeExecCommandContract(description); err == nil {
		t.Fatal("unowned exec_command reference was accepted")
	}

	description = "Run JavaScript.\n\n### `create_goal`\nKeep this."
	stripped, paramsDescription, found, err := stripCodeModeExecCommandContract(description)
	if err != nil || found || paramsDescription != "" || stripped != description {
		t.Fatalf("absent contract = stripped %q, params %q, found %t, error %v", stripped, paramsDescription, found, err)
	}
}

func TestMekugiDirectAdditionalApplyPatchIsRejectedWithoutExecCarrier(t *testing.T) {
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"input": []any{
			map[string]any{
				"type": "additional_tools",
				"role": "developer",
				"tools": []any{
					map[string]any{"type": "custom", "name": "unrelated", "future": true},
					map[string]any{"type": "custom", "name": applyPatchToolName, "description": "Apply a patch.", "future": map[string]any{"kept": true}},
				},
				"future": map[string]any{"kept": true},
			},
			map[string]any{"role": "user", "content": "task"},
		},
		"tools":       []any{map[string]any{"type": "function", "name": "lookup"}},
		"tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session-direct", "thread-direct", metadata, true)
	if err == nil || transform != nil || !strings.Contains(err.Error(), "unsupported flat apply_patch") {
		t.Fatalf("direct rewrite = transform %v, error %v", transform, err)
	}
	if len(proxy.sessions) != 0 {
		t.Fatalf("direct rejection created session resources: %#v", proxy.sessions)
	}
}

func TestMekugiAdditionalToolsReplacementRejectsDuplicateAndConflictingOwners(t *testing.T) {
	execTool := func() map[string]any {
		return map[string]any{"type": "custom", "name": "exec", "description": testCodeModeDescription}
	}
	additional := func(tools ...any) map[string]any {
		return map[string]any{
			"type": "additional_tools",
			"role": "developer",
			"tools": []any{map[string]any{
				"type": "namespace", "name": "functions", "tools": tools,
			}},
		}
	}
	tests := []struct {
		name  string
		input []any
		tools []any
	}{
		{
			name:  "duplicate additional items",
			input: []any{additional(execTool()), additional(execTool())},
		},
		{
			name:  "duplicate exec tools",
			input: []any{additional(execTool(), execTool())},
		},
		{
			name:  "flat and namespaced exec",
			input: []any{testFlatCodeModeAdditionalTools(testCodeModeDescription), additional(execTool())},
		},
		{
			name:  "standalone native collision",
			input: []any{additional(execTool())},
			tools: []any{map[string]any{"type": "custom", "name": applyPatchToolName}},
		},
		{
			name:  "existing mekugi collision",
			input: []any{additional(execTool())},
			tools: []any{map[string]any{"type": "custom", "name": mekugiToolName}},
		},
		{
			name:  "direct namespaced apply_patch collision",
			input: []any{additional(execTool(), map[string]any{"type": "custom", "name": applyPatchToolName})},
		},
		{
			name:  "namespaced mekugi collision",
			input: []any{additional(execTool(), map[string]any{"type": "custom", "name": mekugiToolName})},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]json.RawMessage{
				"input": mustTestJSON(t, test.input),
				"tools": mustTestJSON(t, test.tools),
			}
			beforeInput := bytes.Clone(fields["input"])
			beforeTools := bytes.Clone(fields["tools"])
			_, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), testInstalledTools())
			if err == nil || replaced {
				t.Fatalf("replacement = %v, error %v", replaced, err)
			}
			if !bytes.Equal(fields["input"], beforeInput) || !bytes.Equal(fields["tools"], beforeTools) {
				t.Fatalf("rejected request mutated: %#v", fields)
			}
		})
	}
}

func TestMekugiAdditionalToolsReplacementAlwaysRemovesExecCommand(t *testing.T) {
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{testCodeModeAdditionalTools(testCodeModeDescription)}),
		"tools": mustTestJSON(t, []any{}),
	}

	owner, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), testInstalledTools())
	if err != nil || !replaced || owner != "exec" {
		t.Fatalf("owner = %q, replaced %v, error %v", owner, replaced, err)
	}
	functionsTools, _ := testFunctionsNamespaceTools(t, fields)
	execIndex := slices.IndexFunc(functionsTools, func(tool map[string]json.RawMessage) bool {
		return jsonString(tool, "name") == "exec"
	})
	if execIndex < 0 {
		t.Fatalf("functions namespace lost exec: %#v", functionsTools)
	}
	description := jsonString(functionsTools[execIndex], "description")
	if strings.Contains(description, codeModeApplyPatchHeading) ||
		strings.Contains(description, codeModeExecCommandHeading) ||
		strings.Contains(description, "tools.exec_command") ||
		!strings.Contains(description, "### `create_goal`") {
		t.Fatalf("rewritten carrier description = %q", description)
	}
}

func TestMekugiAdditionalToolsReplacementLeavesUnsupportedAndMalformedRequestsUnchanged(t *testing.T) {
	tests := []struct {
		name       string
		input      json.RawMessage
		tools      json.RawMessage
		toolChoice json.RawMessage
	}{
		{
			name:  "malformed additional tools",
			input: json.RawMessage(`[{"type":"additional_tools","tools":{}}]`),
			tools: mustTestJSON(t, []any{map[string]any{"type": "function", "name": "lookup"}}),
		},
		{
			name:  "unrelated heading collision",
			input: mustTestJSON(t, []any{testCodeModeAdditionalTools("### `apply_patch`\ndocumentation only")}),
			tools: mustTestJSON(t, []any{}),
		},
		{
			name:       "restricted exec choice",
			input:      mustTestJSON(t, []any{testCodeModeAdditionalTools(testCodeModeDescription)}),
			tools:      mustTestJSON(t, []any{}),
			toolChoice: mustTestJSON(t, map[string]any{"type": "custom", "name": "exec"}),
		},
		{
			name:  "unknown future input shape",
			input: json.RawMessage(`{"future":true}`),
			tools: mustTestJSON(t, []any{}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]json.RawMessage{"input": bytes.Clone(test.input), "tools": bytes.Clone(test.tools)}
			if test.toolChoice != nil {
				fields["tool_choice"] = bytes.Clone(test.toolChoice)
			}
			beforeInput := bytes.Clone(fields["input"])
			beforeTools := bytes.Clone(fields["tools"])
			beforeChoice := bytes.Clone(fields["tool_choice"])
			_, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), testInstalledTools())
			if (err != nil) != (test.toolChoice != nil) || replaced {
				t.Fatalf("replacement = %v, error %v", replaced, err)
			}
			if !bytes.Equal(fields["input"], beforeInput) || !bytes.Equal(fields["tools"], beforeTools) || !bytes.Equal(fields["tool_choice"], beforeChoice) {
				t.Fatalf("unsupported request mutated: %#v", fields)
			}
		})
	}
}

func TestMekugiReplacementRetainsNamespacedExecOwnerName(t *testing.T) {
	fields := map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{testCodeModeAdditionalTools(testCodeModeDescription)}),
		"tools": mustTestJSON(t, []any{}),
	}
	got, replaced, err := replaceCodeModeTools(fields, decodeResponsesToolCatalog(fields), testInstalledTools())
	if err != nil || !replaced || got != "exec" {
		t.Fatalf("owner = %q, replaced %v, error %v", got, replaced, err)
	}
}

func TestMekugiPrepareRequestLeavesIneligibleRequestUnchanged(t *testing.T) {
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"input": []any{map[string]any{
			"type":  "additional_tools",
			"tools": []any{map[string]any{"type": "custom", "name": "exec", "description": testCodeModeDescription}},
		}},
		"tools": []any{map[string]any{"type": "function", "name": "lookup"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	beforeInput := bytes.Clone(request.fields["input"])
	beforeTools := bytes.Clone(request.fields["tools"])
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}
	transform, err := proxy.prepareRequest(t.Context(), &request, "", "thread", metadata, true)
	if err == nil || transform != nil || !strings.Contains(err.Error(), "valid session ID") || !bytes.Equal(beforeInput, request.fields["input"]) || !bytes.Equal(beforeTools, request.fields["tools"]) {
		t.Fatalf("ineligible request = transform %v, error %v, fields %#v", transform, err, request.fields)
	}
}

func TestMekugiIneligibleContinuationDoesNotRestoreHistory(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{"call-H": {script: testMekugiScript, patch: testTranslatedPatch}}); err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest([]byte(`{"input":[{"type":"custom_tool_call","name":"apply_patch","call_id":"call-H","input":` + jsonQuoted(testTranslatedPatch) + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(request.fields["input"])
	transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", codexTurnMetadata{}, false)
	if err == nil || transform != nil || !strings.Contains(err.Error(), "valid turn metadata") || !bytes.Equal(before, request.fields["input"]) {
		t.Fatalf("ineligible continuation = transform %v, error %v, input %s", transform, err, request.fields["input"])
	}
}

func TestMekugiTranslationWithoutWorkspaceUsesNoBaseDirectory(t *testing.T) {
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"input": []any{testCodeModeAdditionalTools(testCodeModeDescription)},
	}))
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	translator := mekugiTranslatorFunc(func(_ context.Context, directory, script string) ([]byte, error) {
		calls++
		if directory != "" {
			t.Fatalf("directory = %q, want no base directory", directory)
		}
		if script != testMekugiScript {
			t.Fatalf("script = %q", script)
		}
		return []byte(testTranslatedPatch), nil
	})
	proxy := newManagedMekugiProxy(t, translator)
	transform, err := proxy.prepareRequest(
		t.Context(),
		&request,
		"session-without-workspace",
		"thread-without-workspace",
		codexTurnMetadata{RequestKind: "turn"},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()

	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{testMekugiItem()},
	})); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("translations = %d, want 1", calls)
	}
}

func TestMekugiJSONWrapsPatchAndImmediateReportInCodeModeExec(t *testing.T) {
	calls := 0
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, &calls))
	originalItem := mustTestJSON(t, testMekugiItem())
	payload := mustTestJSON(t, map[string]any{
		"status":      "completed",
		"output":      []any{json.RawMessage(originalItem), map[string]any{"type": "message", "future": true}},
		"tools":       []any{map[string]any{"type": "custom", "name": mekugiToolName}},
		"tool_choice": map[string]any{"type": "custom", "name": mekugiToolName},
		"future":      map[string]any{"kept": true},
	})
	visible, err := transform.TransformJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("translations = %d", calls)
	}
	var response struct {
		Output     []json.RawMessage `json:"output"`
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tools) != 1 || !bytes.Contains(response.Tools[0], []byte(`"name":"lookup"`)) || string(response.ToolChoice) != `"auto"` {
		t.Fatalf("restored response contract = %s", visible)
	}
	var carrier struct {
		CallID string          `json:"call_id"`
		Name   string          `json:"name"`
		Input  string          `json:"input"`
		Future json.RawMessage `json:"future"`
	}
	if err := json.Unmarshal(response.Output[0], &carrier); err != nil {
		t.Fatal(err)
	}
	wantInput := (mekugiHistory{patch: testTranslatedPatch, report: testMekugiReport}).carrierInput()
	if carrier.CallID != "call-H" || carrier.Name != "exec" || carrier.Input != wantInput || string(carrier.Future) != `{"kept":true}` {
		t.Fatalf("translated call = %s", response.Output[0])
	}

	replay, err := parseResponsesRequest([]byte(`{"input":[` + string(response.Output[0]) + `,{"type":"custom_tool_call_output","call_id":"call-H","output":` + jsonQuoted(testMekugiReport) + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(replay.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(items[0], originalItem) {
		t.Fatalf("reconstructed prefix item = %s, want %s", items[0], originalItem)
	}
	if !bytes.Contains(items[1], []byte(`"output":`+jsonQuoted(testMekugiReport))) {
		t.Fatalf("immediate mekugi report changed during replay: %s", items[1])
	}
}

func TestNativeExecCommandAddsShellWarning(t *testing.T) {
	const nativeInput = "const result = await tools.exec_command({\"cmd\":\"printf ok\"});\ntext(result.output);"
	const unrelatedInput = "text(\"ok\");"
	warningInput := misuseWarningProjection(nativeExecCommandWarning)
	wantInput := "const result = await tools.exec_command({\"cmd\":\"printf ok\"});\n" +
		warningInput + codeModeOutputProjection
	rewritten, gotWarning, changed, detected := nativeExecCommandInput(nativeInput)
	if !changed || !detected || rewritten != wantInput || gotWarning != warningInput {
		t.Fatalf(
			"rewrite native exec input: changed %t, detected %t, warning %q\n%s",
			changed,
			detected,
			gotWarning,
			rewritten,
		)
	}
	var arguments struct {
		Command string `json:"cmd"`
	}
	decodeExecCarrierArguments(t, rewritten, &arguments)
	if arguments.Command != "printf ok" {
		t.Fatalf("native warning changed command to %q", arguments.Command)
	}
	repeated, repeatedWarning, repeatedChange, repeatedDetection := nativeExecCommandInput(rewritten)
	if repeatedChange || !repeatedDetection || repeated != rewritten || repeatedWarning != warningInput {
		t.Fatalf(
			"repeated native rewrite: changed %t, detected %t, warning %q\n%s",
			repeatedChange,
			repeatedDetection,
			repeatedWarning,
			repeated,
		)
	}
	combined, _, combinedChange, err := insertExecCommandWarning(rewritten, "functions.shell: warning: secondary warning")
	if err != nil || !combinedChange {
		t.Fatalf("insert second warning: changed %t, error %v", combinedChange, err)
	}
	idempotent, _, duplicateChange, err := insertExecCommandWarning(combined, nativeExecCommandWarning)
	if err != nil || duplicateChange || idempotent != combined {
		t.Fatalf("reinsert first warning: changed %t, error %v\n%s", duplicateChange, err, idempotent)
	}

	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "native", "input": nativeInput},
			map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "unrelated", "input": unrelatedInput},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 2 ||
		jsonString(response.Output[0], "input") != rewritten ||
		jsonString(response.Output[1], "input") != unrelatedInput {
		t.Fatalf("native exec response = %s", visible)
	}

	stream, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	added := mustTestJSON(t, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"type": "custom_tool_call", "id": "item-native", "name": "exec",
			"call_id": "native-stream", "input": "", "status": "in_progress",
		},
	})
	if output, err := stream.TransformSSE(added); err != nil || len(output) != 1 || !bytes.Equal(output[0], added) {
		t.Fatalf("native exec added event = %q, error %v", output, err)
	}
	done := mustTestJSON(t, map[string]any{
		"type": "response.custom_tool_call_input.done", "item_id": "item-native", "input": nativeInput,
	})
	output, err := stream.TransformSSE(done)
	if err != nil || len(output) != 1 {
		t.Fatalf("native exec input.done event = %q, error %v", output, err)
	}
	var event struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(output[0], &event); err != nil || event.Input != rewritten {
		t.Fatalf("native exec input.done event = %q, error %v", output, err)
	}
}

func decodeExecCarrierArguments(t *testing.T, carrierInput string, destination any) {
	t.Helper()
	encoded := strings.TrimPrefix(carrierInput, "const result = await tools.exec_command(")
	before, _, ok := strings.Cut(encoded, ");\n")
	if !ok {
		t.Fatalf("translated exec carrier is malformed: %s", carrierInput)
	}
	if err := json.Unmarshal([]byte(before), destination); err != nil {
		t.Fatalf("decode translated exec arguments: %v\n%s", err, carrierInput)
	}
}

func TestShellJSONTranslatesBashCasesEndToEnd(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)

	tests := []struct {
		name        string
		input       string
		wantCommand string
	}{
		{name: "single external command", input: "foo", wantCommand: "foo"},
		{name: "single external command final newline", input: "foo\n", wantCommand: "foo"},
		{
			name:        "reported external command",
			input:       "rtk shadowtree test . -run='^$'\n",
			wantCommand: "rtk shadowtree test . -run='^$'",
		},
		{
			name:        "explicit bash selector",
			input:       "#!bash\nrtk ok\n",
			wantCommand: `shell bash $'rtk ok\n'`,
		},
		{
			name:        "explicit env bash selector",
			input:       "#!/usr/bin/env bash\nrtk ok\n",
			wantCommand: `shell bash $'rtk ok\n'`,
		},
		{
			name:        "params directive",
			input:       "#!params={\"workdir\":\"/tmp\"}\nrtk ok\n",
			wantCommand: "rtk ok",
		},
		{
			name:        "reported test with exec params",
			input:       "#!params={\"max_output_tokens\":2000}\nrtk go test ./internal/router\n",
			wantCommand: "rtk go test ./internal/router",
		},
		{
			name:        "reported install with tolerated params",
			input:       "# !params {\"yield_time_ms\":1000}\nrtk make install\n",
			wantCommand: "rtk make install",
		},
		{
			name:        "explicit bash with params",
			input:       "#!bash\n#!params={\"workdir\":\"/tmp\"}\nrtk ok\n",
			wantCommand: `shell bash $'rtk ok\n'`,
		},
		{
			name:        "params with private command",
			input:       "#!params={\"workdir\":\"/tmp\"}\nhcat file.txt\n",
			wantCommand: `shell bash $'hcat file.txt\n'`,
		},
		{name: "single private command", input: "hcat file.txt\n"},
		{name: "single shell builtin", input: "printf ok\n"},
		{name: "nested private command", input: "rtk \"$(hcat file.txt)\"\n"},
		{
			name:        "quotes and final newline",
			input:       `printf '"%s\n"' ./* | sed 's#^\./##'` + "\n",
			wantCommand: `shell bash $'printf \'"%s\\n"\' ./* | sed \'s#^\\./##\'\n'`,
		},
		{name: "redundant errexit", input: "set -e\nfoo\n"},
		{
			name:  "multiline",
			input: "printf one\nprintf two\n",
		},
		{
			name:  "meaningful options",
			input: "set -euo pipefail\nfoo\n",
		},
		{
			name:  "multiple commands",
			input: "set -e\nfalse; echo survived\n",
		},
	}
	output := make([]any, 0, len(tests))
	for index, test := range tests {
		output = append(output, map[string]any{
			"type":    "custom_tool_call",
			"id":      fmt.Sprintf("item-shell-%d", index),
			"call_id": fmt.Sprintf("call-shell-%d", index),
			"name":    "shell",
			"input":   test.input,
			"status":  "completed",
		})
	}
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": output,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != len(tests) {
		t.Fatalf("translated shell output count = %d, want %d: %s", len(response.Output), len(tests), visible)
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := response.Output[index]
			if jsonString(item, "name") != "exec" {
				t.Fatalf("translated carrier name = %q", jsonString(item, "name"))
			}
			carrierInput := jsonString(item, "input")
			var arguments struct {
				Command string `json:"cmd"`
			}
			decodeExecCarrierArguments(t, carrierInput, &arguments)
			want := test.wantCommand
			if want == "" {
				want = workerCommand("shell", []string{"bash", test.input})
			}
			if arguments.Command != want {
				t.Fatalf("translated exec command = %q, want %q", arguments.Command, want)
			}
		})
	}
}

func TestDirectBashExecCommand(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
		ok        bool
	}{
		{name: "external", arguments: []string{"bash", "rtk ok\n"}, want: "rtk ok", ok: true},
		{name: "quoted external", arguments: []string{"bash", "'rtk' ok\n"}, want: "'rtk' ok", ok: true},
		{name: "external redirection", arguments: []string{"bash", "rtk ok >out\n"}, want: "rtk ok >out", ok: true},
		{name: "hrun", arguments: []string{"bash", "hrun --max-tokens 100 --tail -- echo done\n"}},
		{name: "quoted hrun", arguments: []string{"bash", "'hrun' --max-tokens 100 -- echo done\n"}},
		{name: "private", arguments: []string{"bash", "hcat file.txt\n"}},
		{name: "builtin", arguments: []string{"bash", "printf ok\n"}},
		{name: "journal", arguments: []string{"bash", "journal add 'Running check'\n"}},
		{name: "dynamic command", arguments: []string{"bash", "$command ok\n"}},
		{name: "command substitution", arguments: []string{"bash", "rtk \"$(hcat file.txt)\"\n"}},
		{name: "process substitution", arguments: []string{"bash", "rtk <(hcat file.txt)\n"}},
		{name: "negated", arguments: []string{"bash", "! rtk ok\n"}},
		{name: "background", arguments: []string{"bash", "rtk ok &\n"}},
		{name: "semicolon", arguments: []string{"bash", "rtk ok;\n"}},
		{name: "pipeline", arguments: []string{"bash", "rtk ok | cat\n"}},
		{name: "multiple lines", arguments: []string{"bash", "rtk one\nrtk two\n"}},
		{name: "malformed", arguments: []string{"bash", "if\n"}},
		{name: "interpreter arguments", arguments: []string{"bash", "-x", "rtk ok\n"}},
		{name: "other interpreter", arguments: []string{"sh", "rtk ok\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := proxy.registry.directBashExecCommand(test.arguments)
			if got != test.want || ok != test.ok {
				t.Fatalf("directBashExecCommand(%q) = %q, %t; want %q, %t", test.arguments, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestWorkerCommandBashRoundTripsQuotedArgument(t *testing.T) {
	registry := sharedProxyTestRegistry(t)

	argument := "printf '\"%s\\n\"' ./* | sed 's#^\\./##'\n"
	stdout, stderr, exitCode := runShellWorkerTest(
		t,
		registry,
		"bash",
		nil,
		workerCommand("printf", []string{"%s", argument}),
		nil,
	)
	if exitCode != 0 || stdout != argument || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
	}
}

func TestShellInterpreterWrapperAddsWarning(t *testing.T) {
	contribution := toolContribution{PluginID: "builtin.shell", Name: "shell"}
	for _, test := range []struct {
		input  string
		want   string
		misuse shellWrapperMisuse
	}{
		{
			input:  "python3 - <<'PY'\nprint('ok')\nPY",
			want:   "functions.shell: warning: remove the `python3 - <<...` heredoc wrapper; start the script with `#!python3` and put the Python program directly in the body",
			misuse: shellWrapperMisuse{Kind: "heredoc", Interpreter: "python3", wrapper: "- <<"},
		},
		{
			input:  "python3 -I -c 'print(1)'",
			want:   "functions.shell: warning: replace `python3 -I -c ...` with `#!python3 -I` on the first line and put the Python program directly in the body",
			misuse: shellWrapperMisuse{Kind: "-c", Interpreter: "python3", InterpreterArgs: []string{"-I"}, wrapper: "-c"},
		},
		{
			input:  "node --input-type=module -e 'console.log(1)'",
			want:   "functions.shell: warning: replace `node --input-type=module -e ...` with `#!node --input-type=module` on the first line and put the JavaScript program directly in the body",
			misuse: shellWrapperMisuse{Kind: "-e", Interpreter: "node", InterpreterArgs: []string{"--input-type=module"}, wrapper: "-e"},
		},
		{
			input:  "bash -c 'printf ok'",
			want:   "functions.shell: warning: remove the `bash -c` wrapper and submit the Bash script body directly without a shebang",
			misuse: shellWrapperMisuse{Kind: "-c", Interpreter: "bash", wrapper: "-c"},
		},
		{
			input:  "sh -ec 'printf ok'",
			want:   "functions.shell: warning: replace `sh -ec ...` with `#!sh -e` on the first line and put the program directly in the body",
			misuse: shellWrapperMisuse{Kind: "-c", Interpreter: "sh", InterpreterArgs: []string{"-e"}, wrapper: "-ec"},
		},
		{
			input:  "/usr/bin/python3 -c 'print(1)'",
			want:   "functions.shell: warning: replace `python3 -c ...` with `#!python3` on the first line and put the Python program directly in the body",
			misuse: shellWrapperMisuse{Kind: "-c", Interpreter: "python3", wrapper: "-c"},
		},
		{
			input:  "psql --command 'select 1'",
			want:   "functions.shell: warning: replace `psql --command ...` with `#!psql` on the first line and put the program directly in the body",
			misuse: shellWrapperMisuse{Kind: "--command", Interpreter: "psql", wrapper: "--command"},
		},
	} {
		misuses := shellInterpreterWrapperMisuses(contribution, test.input)
		if len(misuses) != 1 {
			t.Errorf("misuses for %q = %#v; want %#v", test.input, misuses, test.misuse)
			continue
		}
		misuse := misuses[0]
		if misuse.Kind != test.misuse.Kind || misuse.Interpreter != test.misuse.Interpreter ||
			!slices.Equal(misuse.InterpreterArgs, test.misuse.InterpreterArgs) || misuse.wrapper != test.misuse.wrapper {
			t.Errorf("misuse for %q = %#v; want %#v", test.input, misuse, test.misuse)
			continue
		}
		if warning := shellInterpreterWrapperWarning(misuse); warning != test.want {
			t.Errorf("warning for %q = %q; want %q", test.input, warning, test.want)
		}
	}

	for _, input := range []string{
		"python3 -c 'print(1)'",
		"python3 -I -c 'print(1)'",
		"node -e 'console.log(1)'",
		"node --input-type=module -e 'console.log(1)'",
		"bash -c 'printf ok'",
		"bash -lc 'printf ok'",
		"bash -x -c 'printf ok'",
		"python3 <<'PY'\nprint('ok')\nPY",
		"export PYTHONDONTWRITEBYTECODE=1\npython3 - <<'PY'\nprint('ok')\nPY",
		"env PYTHONDONTWRITEBYTECODE=1 python3 - <<'PY'\nprint('ok')\nPY",
		"PYTHONDONTWRITEBYTECODE=1 python3 - <<'PY'\nprint('ok')\nPY",
		"printf before\npython3 - <<'PY'\nprint('ok')\nPY",
		"/usr/bin/python3 -c 'print(1)'",
		"pypy3 -c 'print(1)'",
		"pypy3 - <<'PY'\nprint('ok')\nPY",
		"nodejs - <<'JS'\nconsole.log(1)\nJS",
		"bun -e 'console.log(1)'",
		"bun - <<'JS'\nconsole.log(1)\nJS",
		"env NODE_NO_WARNINGS=1 node - <<'JS'\nconsole.log(1)\nJS",
		"bash - <<'SH'\nprintf ok\nSH",
		"sh -ec 'printf ok'",
		"sh - <<'SH'\nprintf ok\nSH",
		"zsh -c 'printf ok'",
		"fish -c 'printf ok'",
		"perl -e 'print 1'",
		"ruby -e 'puts 1'",
		"php -r 'echo 1;'",
		"lua -e 'print(1)'",
		"psql -c 'select 1'",
		"psql --command 'select 1'",
		"mysql -e 'select 1'",
		"mysql --execute 'select 1'",
	} {
		misuses := shellInterpreterWrapperMisuses(contribution, input)
		if len(misuses) == 0 || shellInterpreterWrapperWarning(misuses[0]) == "" {
			t.Errorf("interpreter wrapper was not detected: %q", input)
		}
	}
	for _, input := range []string{
		"cat <<'EOF'\nhello\nEOF",
		"cat input.txt <<'EOF'\nhello\nEOF",
		"printf '%s' 'normal command merely contains node -e'",
		"# example: python3 -c 'print(1)'",
		"#!python3\nprint(\"python3 -c and <<'EOF'\")",
		"#!node\nconsole.log(\"ruby -e <<EOF\")",
		"python3 script.py <<'EOF'\ndata\nEOF",
		"python3 -m module <<'EOF'\ndata\nEOF",
		"printf '%s' \"$(printf 'node -e')\"",
		"node -r fs --version",
		"python3 -W -c --version",
		"python3 script.py",
		"python3 -m module",
		"python3 -I script.py",
		"node app.js",
		"node --trace-warnings app.js",
		"bash script.sh",
		"bash -x script.sh",
		"git -c key=value status",
		"grep -e pattern file",
		"#!cat\ncat shebang executes",
		"printf ok",
	} {
		if misuses := shellInterpreterWrapperMisuses(contribution, input); len(misuses) != 0 {
			t.Errorf("ordinary shell input misuses = %#v for %q", misuses, input)
		}
	}
	if misuses := shellInterpreterWrapperMisuses(
		toolContribution{PluginID: "configured", Name: "shell"},
		"python3 -c pass",
	); len(misuses) != 0 {
		t.Error("configured shell plugin received interpreter-wrapper warning")
	}

	const input = "printf before\npython3 -c 'print(1)'"
	misuses := shellInterpreterWrapperMisuses(contribution, input)
	if len(misuses) == 0 {
		t.Fatal("interpreter command wrapper was not detected")
	}
	warningInput := misuseWarningProjection(shellInterpreterWrapperWarning(misuses[0]))
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{map[string]any{
			"type": "custom_tool_call", "id": "item-shell", "call_id": "call-shell",
			"name": "shell", "input": input, "status": "completed",
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || jsonString(response.Output[0], "name") != "exec" {
		t.Fatalf("warned shell carrier = %s", visible)
	}
	carrierInput := jsonString(response.Output[0], "input")
	var arguments struct {
		Command string `json:"cmd"`
	}
	decodeExecCarrierArguments(t, carrierInput, &arguments)
	wantCommand := workerCommand("shell", []string{"bash", input})
	if arguments.Command != wantCommand || strings.Contains(arguments.Command, warningInput) ||
		!strings.Contains(carrierInput, warningInput+codeModeMetadataProjection) {
		t.Fatalf("warned shell carrier = %q, command %q, want original input %q", carrierInput, arguments.Command, input)
	}
}

func TestShellLiteralExamplesDoNotWarnThroughRouter(t *testing.T) {
	for _, input := range []string{
		"node -r fs --version",
		"python3 -W -c --version",
		"#!python3\nprint(\"python3 -c and <<'EOF'\")",
		"printf '%s' 'python3 -c and <<EOF'",
		"cat <<'EOF'\npython3 -c 'example'\nEOF",
	} {
		t.Run(input, func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"status": "completed",
				"output": []any{map[string]any{
					"type": "custom_tool_call", "id": "item-shell", "call_id": "call-shell",
					"name": "shell", "input": input, "status": "completed",
				}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(visible), "functions.shell: warning:") {
				t.Fatalf("source data produced a warning: %s", visible)
			}
		})
	}
}

func TestShellCommandDataHeredocDoesNotAddWarning(t *testing.T) {
	misuses := shellInterpreterWrapperMisuses(
		toolContribution{PluginID: builtinToolsPluginID, Name: "shell"},
		"python3 -c 'print(1)' <<'EOF'\ndata\nEOF",
	)
	if len(misuses) != 1 || misuses[0].Kind != "-c" {
		t.Fatalf("command-mode input produced extra warnings: %#v", misuses)
	}
}

func TestShellStacksDistinctMisuseWarnings(t *testing.T) {
	contribution := toolContribution{PluginID: "builtin.shell", Name: "shell"}
	wrapperMisuses := shellInterpreterWrapperMisuses(
		contribution,
		"python3 -c 'print(1)'\nnode -e 'console.log(1)'\nruby -e 'puts 1'",
	)
	if len(wrapperMisuses) != 2 || wrapperMisuses[0].Kind != "-c" || wrapperMisuses[1].Kind != "-e" {
		t.Fatalf("distinct wrapper misuses = %#v, want -c then -e", wrapperMisuses)
	}
	const script = "bun -e 'console.log(1)'\npython3 - <<'PY'\nprint('ok')\nPY"
	misuses := shellInterpreterWrapperMisuses(contribution, script)
	if len(misuses) != 2 || misuses[0].Kind != "-e" || misuses[1].Kind != "heredoc" {
		t.Fatalf("stacked shell misuses = %#v, want -e then heredoc", misuses)
	}
	wrapperInput := misuseWarningProjection(shellInterpreterWrapperWarning(misuses[0]))
	heredocInput := misuseWarningProjection(shellInterpreterWrapperWarning(misuses[1]))

	call := func(t *testing.T, input string) string {
		t.Helper()
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type": "custom_tool_call", "id": "item-shell", "call_id": "call-shell",
				"name": "shell", "input": input, "status": "completed",
			}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Output []map[string]json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(visible, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Output) != 1 || jsonString(response.Output[0], "name") != "exec" {
			t.Fatalf("stacked shell carrier = %s", visible)
		}
		return jsonString(response.Output[0], "input")
	}

	direct := call(t, script)
	if !strings.Contains(direct, wrapperInput+heredocInput+codeModeMetadataProjection) {
		t.Fatalf("direct shell warnings did not stack in order: %q", direct)
	}

	command := "curl -fsSL 'https://example.com' |\n" + script
	recoveredInput := "const result = await tools.exec_command({\"cmd\":" +
		jsonQuoted(command) +
		",\"login\":false});\ntext(JSON.stringify(result));"
	recovered := call(t, recoveredInput)
	wantPrefix := misuseWarningProjection(shellCodeModeRecoveryWarning) + misuseWarningProjection(nativeExecCommandWarning)
	if recovered != wantPrefix+recoveredInput {
		t.Fatalf("recovered shell warnings did not stack in order:\n%s", recovered)
	}
}

func TestWorkerTemplateExecInputQuotesNestedShellCommand(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	shell, ok := proxy.registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	shellArguments := []string{"python3", `print('{"hello":"world"}')`}
	carrierInput, err := proxy.registry.execCarrierPayload(
		codeModeCarrierCustom,
		shell,
		"",
		shellArguments,
		"curl -fsSL URL | {.} | jq",
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var carrierArguments struct {
		Command string `json:"cmd"`
	}
	decodeExecCarrierArguments(t, carrierInput, &carrierArguments)
	want := "curl -fsSL URL | " + workerCommand("shell", shellArguments) + " | jq"
	if carrierArguments.Command != want {
		t.Fatalf("translated template command = %q, want %q", carrierArguments.Command, want)
	}

	for _, template := range []string{"missing", "{.} then {.}"} {
		if _, err := proxy.registry.execCarrierPayload(codeModeCarrierCustom, shell, "", []string{"bash", ""}, template, nil, nil); err == nil {
			t.Fatalf("worker template %q did not reject", template)
		}
	}
}

func TestShellCarrierUsesFixedHelperForBuiltin(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	shell, ok := proxy.registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	carrierInput, err := proxy.registry.execCarrierPayload(codeModeCarrierCustom, shell, "printf ok", []string{"bash", "printf ok"}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var arguments struct {
		Command string `json:"cmd"`
	}
	decodeExecCarrierArguments(t, carrierInput, &arguments)
	want := workerCommand("shell", []string{"bash", "printf ok"})
	if arguments.Command != want {
		t.Fatalf("shell helper command = %q, want %q", arguments.Command, want)
	}
}

func TestWorkerExecInputMergesValidatedParams(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	shell, ok := proxy.registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	const sourceInput = "#!params={\"workdir\":\"/tmp/example\",\"tty\":true,\"login\":false}\nrtk ok\n"
	carrierInput, err := proxy.registry.execCarrierPayload(codeModeCarrierCustom, shell, sourceInput, []string{"bash", "rtk ok\n"}, "", map[string]json.RawMessage{
		"workdir": mustMarshalJSON("/tmp/example"),
		"tty":     mustMarshalJSON(true),
		"login":   mustMarshalJSON(false),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(carrierInput, "tools.exec_command(") != 1 || strings.Contains(carrierInput, "write_stdin") {
		t.Fatalf("shell carrier did not preserve one yielded execution: %s", carrierInput)
	}
	var arguments struct {
		Command string `json:"cmd"`
		Workdir string `json:"workdir"`
		TTY     bool   `json:"tty"`
		Login   bool   `json:"login"`
	}
	decodeExecCarrierArguments(t, carrierInput, &arguments)
	if arguments.Command != "rtk ok" || arguments.Workdir != "/tmp/example" ||
		!arguments.TTY || arguments.Login {
		t.Fatalf("translated exec arguments = %+v", arguments)
	}
	if _, err := proxy.registry.execCarrierPayload(codeModeCarrierCustom, shell, "", []string{"bash", ""}, "", map[string]json.RawMessage{
		"cmd": mustMarshalJSON("forbidden"),
	}, nil); err == nil {
		t.Fatal("exec params accepted cmd")
	}
	if _, err := proxy.registry.execCarrierPayload(codeModeCarrierCustom, shell, "", []string{"bash", ""}, "", map[string]json.RawMessage{
		"login": mustMarshalJSON(true),
	}, nil); err == nil {
		t.Fatal("exec params accepted login true")
	}
}

func TestShellExecCarriersForwardNativeResultWithoutPolling(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	shell, ok := proxy.registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	carrierInput, err := proxy.registry.execCarrierPayload(
		codeModeCarrierCustom,
		shell,
		"",
		[]string{"python3", "print('ok')"},
		"before | {.} | after",
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(carrierInput, "tools.exec_command(") != 1 ||
		!strings.HasSuffix(carrierInput, "text(JSON.stringify(result));") ||
		strings.Contains(carrierInput, "write_stdin") {
		t.Fatalf("shell template carrier did not forward one native result: %s", carrierInput)
	}

	registry, _ := newToolPluginTestRegistry(t)
	plugin, ok := registry.contribution("plugin_tool")
	if !ok {
		t.Fatal("configured contribution is unavailable")
	}
	plainInput, err := registry.execCarrierPayload(codeModeCarrierCustom, plugin, "", []string{"line.txt"}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plainInput, "text(result.output);") {
		t.Fatalf("non-shell carrier output projection changed: %s", plainInput)
	}
}

func TestMekugiHistoryDoesNotCrossWorkspacesSharingSessionIdentity(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	requestFor := func(t *testing.T, extraItems ...any) parsedResponsesRequest {
		t.Helper()
		items := []any{testCodeModeAdditionalTools(testCodeModeDescription)}
		items = append(items, extraItems...)
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
			"input":               items,
			"tools":               []any{},
			"tool_choice":         "auto",
			"parallel_tool_calls": true,
		}))
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	metadataFor := func(workspace string) codexTurnMetadata {
		return codexTurnMetadata{
			RequestKind: "turn",
			Directories: map[string]json.RawMessage{workspace: nil},
		}
	}

	firstWorkspace := t.TempDir()
	firstRequest := requestFor(t)
	first, err := proxy.prepareRequest(t.Context(), &firstRequest, "shared-cache-key", "shared-thread", metadataFor(firstWorkspace), true)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := first.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{testMekugiItem()},
	}))
	first.Close()
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 {
		t.Fatalf("translated output = %s", visible)
	}

	secondWorkspace := t.TempDir()
	secondRequest := requestFor(t, response.Output[0])
	second, err := proxy.prepareRequest(t.Context(), &secondRequest, "shared-cache-key", "shared-thread", metadataFor(secondWorkspace), true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	var replayed []map[string]json.RawMessage
	if err := json.Unmarshal(secondRequest.fields["input"], &replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed input = %s", secondRequest.fields["input"])
	}
	if name := jsonString(replayed[1], "name"); name != "exec" {
		t.Fatalf("cross-workspace replay restored tool name %q", name)
	}
	if input := jsonString(replayed[1], "input"); input != (mekugiHistory{patch: testTranslatedPatch, report: testMekugiReport}).carrierInput() {
		t.Fatalf("cross-workspace replay restored input %q", input)
	}

	history, err := second.translateRecovery("call-recovery", "C1:ffff not-a-target", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(history.translationError, "no rejected HPATCH script to recover") {
		t.Fatalf("cross-workspace recovery history = %+v", history)
	}
}

func TestMekugiReplayPreservesImmediateApplyFailure(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	history := mekugiHistory{script: testMekugiScript, patch: testTranslatedPatch, carrierName: "exec", report: testMekugiReport}
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{"call-H": history}); err != nil {
		t.Fatal(err)
	}
	const applyFailure = "Failed to find expected lines in created.txt:\nmissing\n"
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{
		map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "call-H", "input": history.carrierInput()},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call-H", "output": applyFailure},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&request, "session"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(request.fields["input"], []byte(jsonQuoted(applyFailure))) || bytes.Contains(request.fields["input"], []byte(jsonQuoted(testMekugiReport))) {
		t.Fatalf("apply failure changed during replay: %s", request.fields["input"])
	}
}

func TestMekugiTranslationRewritesConfirmedTargetAlias(t *testing.T) {
	var translatedScript string
	translator := mekugiResultTranslatorFunc(func(_ context.Context, _ string, script string) (mekugiTranslationResult, error) {
		translatedScript = script
		return mekugiTranslationResult{patch: []byte(testTranslatedPatch), report: testMekugiReport}, nil
	})
	transform, _, _, _ := newMekugiTestTransform(t, translator)
	alias := mekugi.TargetAlias{Path: "file.txt", Before: "2:1111", After: "3:2222"}
	transform.visible = map[string]mekugiHistory{"call-first": {root: transform.directory, report: testMekugiReport, confirmed: true, aliases: []mekugi.TargetAlias{alias}}}

	emitted := "in file.txt\ntype 2:1111 \"replacement\""
	if _, err := transform.translate("call-next", emitted, nil); err != nil {
		t.Fatal(err)
	}
	if want := "in file.txt\ntype 3:2222 \"replacement\""; translatedScript != want {
		t.Fatalf("translated script = %q, want %q", translatedScript, want)
	}

	malformed := "in file.txt\ntype 2:1111 \"unterminated"
	if _, err := transform.translate("call-malformed", malformed, nil); err != nil {
		t.Fatal(err)
	}
	if translatedScript != malformed {
		t.Fatalf("malformed script = %q, want evaluator input %q", translatedScript, malformed)
	}
}

func TestMekugiReplayRejectsChangedExecCarrierAndIgnoresUnrelatedCalls(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	history := mekugiHistory{script: testMekugiScript, patch: testTranslatedPatch, carrierName: "exec", report: testMekugiReport}
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{"call-H": history}); err != nil {
		t.Fatal(err)
	}
	changed, _ := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{map[string]any{
		"type": "custom_tool_call", "name": "exec", "call_id": "call-H", "input": "changed",
	}}}))
	if err := proxy.reconcileInputPrefix(&changed, "session"); err == nil {
		t.Fatal("changed replay was accepted")
	}
	unrelated, _ := parseResponsesRequest([]byte(`{"input":[{"type":"custom_tool_call","name":"exec","call_id":"native","input":"unchanged"}]}`))
	before := bytes.Clone(unrelated.fields["input"])
	if err := proxy.reconcileInputPrefix(&unrelated, "session"); err != nil || !bytes.Equal(before, unrelated.fields["input"]) {
		t.Fatalf("unrelated call changed to %s, error %v", unrelated.fields["input"], err)
	}
}

func TestMekugiReportSeparatesHookWarning(t *testing.T) {
	if got := mekugiReport("in file.txt 1:1", "mekugi: warning: hook failed\n"); got != "in file.txt 1:1\nmekugi: warning: hook failed\n" {
		t.Fatalf("mekugiReport() = %q", got)
	}
}

func TestMekugiExecInputQuotesPatchReportAndDiagnostic(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: quoted.txt\n+` ${value} \\\"\n*** End Patch\n"
	report := "in quoted.txt 1:14\n1 ` ${value} \\\"\n"
	input := (mekugiHistory{patch: patch, report: report}).carrierInput()
	if !strings.HasPrefix(input, mekugiApplyExecMarker) || !strings.Contains(input, strconv.Quote(patch)) || !strings.Contains(input, strconv.Quote(report)) {
		t.Fatalf("unsafe or incomplete apply wrapper: %q", input)
	}
	diagnostic := "selector `x` rejected: ${value} " + string([]byte{'\\'})
	if got := (mekugiHistory{translationError: diagnostic}).carrierInput(); got != "text("+strconv.Quote(diagnostic)+");" {
		t.Fatalf("diagnostic wrapper = %q", got)
	}
}

func TestMekugiAlreadySatisfiedUsesDiagnosticCarrier(t *testing.T) {
	translator := mekugiResultTranslatorFunc(func(context.Context, string, string) (mekugiTranslationResult, error) {
		return mekugiTranslationResult{
			report: "in file.txt\nlast none\n",
			change: mekugi.HostChange{AlreadySatisfied: true},
		}, nil
	})
	transform, _, _, _ := newMekugiTestTransform(t, translator)
	history, err := transform.translate("call-noop", "in file.txt\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !history.alreadySatisfied || strings.Contains(history.carrierInput(), "apply_patch") ||
		history.carrierInput() != "text("+strconv.Quote("in file.txt\nlast none\n")+");" {
		t.Fatalf("already-satisfied history = %+v, carrier = %s", history, history.carrierInput())
	}
}

func TestMekugiStreamingReplacesLifecycleWithoutChangingCallID(t *testing.T) {
	calls := 0
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, &calls))
	item := testMekugiItem()
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""

	visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added}))
	if err != nil || visible != nil {
		t.Fatalf("buffered added = %q, error %v", visible, err)
	}
	visible, err = transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "item-H", "delta": "secret"}))
	if err != nil || len(visible) != 1 || string(visible[0]) != `{"type":"response.in_progress"}` {
		t.Fatalf("delta = %q, error %v", visible, err)
	}
	visible, err = transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testMekugiScript}))
	if err != nil || len(visible) != 2 || !bytes.Contains(visible[0], []byte(`"name":"exec"`)) || !bytes.Contains(visible[0], []byte(`"call_id":"call-H"`)) || !bytes.Contains(visible[1], []byte(jsonQuoted((mekugiHistory{patch: testTranslatedPatch, report: testMekugiReport}).carrierInput()))) {
		t.Fatalf("input.done = %q, error %v", visible, err)
	}
	visible, err = transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item}))
	if err != nil || len(visible) != 1 || !bytes.Contains(visible[0], []byte(`"call_id":"call-H"`)) {
		t.Fatalf("item.done = %q, error %v", visible, err)
	}
	completed := map[string]any{"status": "completed", "output": []any{item}}
	visible, err = transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": completed}))
	if err != nil || len(visible) != 1 || calls != 1 {
		t.Fatalf("completed = %q, translations %d, error %v", visible, calls, err)
	}
}

func TestMekugiBufferedDeltaKeepsDownstreamSSEActiveWithoutLeakingInput(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""
	if visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
		"type": "response.output_item.added", "item": added,
	})); err != nil || visible != nil {
		t.Fatalf("buffered added = %q, error %v", visible, err)
	}

	delta := mustTestJSON(t, map[string]any{
		"type": "response.custom_tool_call_input.delta", "item_id": "item-H", "delta": "secret",
	})
	var output bytes.Buffer
	state, err := writeSSEEvent(
		&output,
		[]string{"event: response.custom_tool_call_input.delta\n", "data: " + string(delta) + "\n"},
		"\n",
		transform,
		nil,
	)
	if err != nil || state != responseTerminalPending {
		t.Fatalf("terminal state = %v, error %v", state, err)
	}
	if got := output.String(); got != "event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\n" {
		t.Fatalf("downstream progress event = %q", got)
	}
}

func TestNonMekugiHistoryIsExcludedFromRecovery(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	err := proxy.rememberBatch("session", map[string]mekugiHistory{
		"call-H": {
			toolName: mekugiToolName, script: testMekugiScript,
			translationError: "rejected", evaluatorRejected: true, sequence: 1,
		},
		"call-S": {
			toolName: "shell", script: `hcat file.txt`,
			report: "8ed3: alpha\n", sequence: 2,
		},
		"call-S2": {
			toolName: "shell", script: `hsymbol refs file.go 1:8ed3 Alpha`,
			sequence: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := proxy.recoverableHistory("session")
	if err != nil {
		t.Fatal(err)
	}
	if history.toolName != mekugiToolName || history.script != testMekugiScript {
		t.Fatalf("recoverable history = %+v", history)
	}

	transform := &mekugiResponseTransform{
		proxy:            proxy,
		sessionID:        "session",
		historySessionID: "session",
		visible:          map[string]mekugiHistory{"call-H": history},
		local: map[string]mekugiHistory{
			"call-local-shell": {
				toolName: "shell",
				script:   `hgrep alpha .`,
				sequence: 1,
			},
		},
	}
	history, err = transform.recoveryHistory()
	if err != nil {
		t.Fatal(err)
	}
	if history.toolName != mekugiToolName || history.script != testMekugiScript {
		t.Fatalf("recovery after local read-only call = %+v", history)
	}
}

func TestMekugiNonEvaluatorFailureDoesNotBecomeRecoveryBaseline(t *testing.T) {
	calls := 0
	transform, _, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		calls++
		return nil, errors.New("translator failed")
	}))
	first, err := transform.translate("call-1", testMekugiScript, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.evaluatorRejected || strings.Contains(first.translationError, "Use mekugi without `in`") {
		t.Fatalf("non-evaluator failure exposed recovery guidance: %+v", first)
	}
	second, err := transform.translateRecovery("call-2", "C1:ffff not-a-target", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !second.unevaluated ||
		!strings.Contains(second.translationError, "did not produce an evaluator rejection") ||
		calls != 1 {
		t.Fatalf("recovery after non-evaluator failure = %+v, translator calls %d", second, calls)
	}
}

func TestMekugiUnevaluatedRecoveryRunsOutcomeHookOnce(t *testing.T) {
	dataDirectory := t.TempDir()
	outcomePath := filepath.Join(t.TempDir(), "outcome.txt")
	settings := fmt.Sprintf(
		`{"hooks":{"outcome":["printf '%%s' {{shellquote .Stage}}'|'{{shellquote .Outcome}}'|'{{shellquote .ToolName}}'|'{{.EmittedBytes}}'|'{{.EvaluatedBytes}} > %s"]}}`,
		shellQuoteArgument(outcomePath),
	)
	if err := os.WriteFile(filepath.Join(dataDirectory, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	transform, _, _, _ := newMekugiTestTransformWithProxy(
		t,
		newManagedMekugiProxyWithDataDirectory(t, newInProcessMekugiTranslator(dataDirectory), dataDirectory),
	)
	payload := "C1:ffff not-a-target"
	history, err := transform.translateRecovery("call-recovery", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !history.unevaluated {
		t.Fatalf("recovery history = %+v", history)
	}
	got, err := os.ReadFile(outcomePath)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("unevaluated|rejected|hpatch_recover|%d|0", len(payload))
	if string(got) != want {
		t.Fatalf("outcome hook = %q, want %q", got, want)
	}
}

func TestMekugiRecoveryRetainsCorrelationAndRebuildsBeforeTranslation(t *testing.T) {
	base := "in file.txt\ntype 1:aaaa \"payload\"\n"
	want := "in file.txt\ntype 2:bbbb \"payload\"\n"
	calls := 0
	var evaluated string
	translator := mekugiResultTranslatorFunc(func(_ context.Context, _ string, script string) (mekugiTranslationResult, error) {
		calls++
		if calls == 1 {
			return mekugiTranslationResult{
				diagnostic: "type: command 2, reason row-stale: rejected\n",
				rejections: []mekugi.HostRejection{{Command: 2, SourceLine: 2, Operation: "type", Target: "line", Reason: "row-stale"}},
			}, errors.New("rejected")
		}
		evaluated = script
		return mekugiTranslationResult{patch: []byte(testTranslatedPatch)}, nil
	})
	transform, proxy, _, _ := newMekugiTestTransform(t, translator)
	first, err := transform.translate("call-1", base, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := recoveryCommands(base)[1].handle + " 2:bbbb\n"
	second, err := transform.translateRecovery("call-2", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.correlationID != "call-1" || first.attempt != 1 ||
		second.translationError != "" || second.correlationID != "call-1" || second.attempt != 2 {
		t.Fatalf("recovery metadata: first=%+v second=%+v", first, second)
	}
	if calls != 2 || evaluated != want {
		t.Fatalf("translations = %d, evaluated script = %q, want %q", calls, evaluated, want)
	}
	if _, ok := proxy.history(transform.historySessionID, "call-1"); ok {
		t.Fatal("local history committed before response completion")
	}
}

func TestMekugiRecoveryRerejectionExposesCurrentHandles(t *testing.T) {
	base := "in file.txt\ntype 1:aaaa \"value\"\n"
	rebuilt := "in file.txt\ntype 2:bbbb \"value\"\n"
	translator := mekugiResultTranslatorFunc(func(_ context.Context, _ string, _ string) (mekugiTranslationResult, error) {
		return mekugiTranslationResult{
			diagnostic: "type: command 2, reason row-stale: rejected\n",
			rejections: []mekugi.HostRejection{{Command: 2, SourceLine: 2, Operation: "type", Target: "line", Reason: "row-stale"}},
		}, errors.New("rejected")
	})
	transform, _, _, _ := newMekugiTestTransform(t, translator)
	first, err := transform.translate("call-1", base, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := recoveryCommands(base)[1].handle + " 2:bbbb\n"
	second, err := transform.translateRecovery("call-2", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, history := range []mekugiHistory{first, second} {
		if strings.Count(history.translationError, "Rejected target commands:") != 1 ||
			strings.Contains(history.translationError, "Use mekugi without `in`") ||
			strings.Contains(history.translationError, "accept") {
			t.Fatalf("recovery guidance = %q", history.translationError)
		}
	}
	if !strings.Contains(second.translationError, "This re-rejection changed no workspace file") ||
		!strings.Contains(second.translationError, "Earlier C... handles are stale") {
		t.Fatalf("re-rejection lacks stale-handle guidance:\n%s", second.translationError)
	}
	if want := recoveryCommands(rebuilt)[1].handle; !strings.Contains(second.translationError, want) {
		t.Fatalf("re-rejection lacks current command handle %q:\n%s", want, second.translationError)
	}
}

func TestMekugiRecoveryFixesAllEmittedTargetsAtomically(t *testing.T) {
	base := "in first.go\n" +
		"type 1:aaaa \"first\"\n" +
		"in second.go\n" +
		"type 2:bbbb..4:cccc \"second\"\n"
	want := "in first.go\n" +
		"type 3:dddd \"first\"\n" +
		"in second.go\n" +
		"type 5:eeee..7:ffff \"second\"\n"
	rejections := []mekugi.HostRejection{
		{Command: 2, SourceLine: 2, Operation: "type", Target: "line", Reason: "row-stale"},
		{Command: 4, SourceLine: 4, Operation: "type", Target: "range", Reason: "row-stale"},
	}

	calls := 0
	var evaluated string
	transform, _, _, _ := newMekugiTestTransform(t, mekugiResultTranslatorFunc(func(_ context.Context, _ string, script string) (mekugiTranslationResult, error) {
		calls++
		if calls == 1 {
			return mekugiTranslationResult{
				diagnostic: "type: command 2, reason row-stale: rejected\n" +
					"type: command 4, reason row-stale: rejected\n",
				rejections: rejections,
			}, errors.New("rejected")
		}
		evaluated = script
		return mekugiTranslationResult{patch: []byte(testTranslatedPatch)}, nil
	}))
	first, err := transform.translate("call-1", base, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstCommands := recoveryCommands(base)
	for _, want := range []string{firstCommands[1].handle, firstCommands[3].handle} {
		if !strings.Contains(first.translationError, want) {
			t.Fatalf("guidance lacks command handle %q:\n%s", want, first.translationError)
		}
	}

	payload := strings.Join([]string{
		firstCommands[1].handle + " 3:dddd",
		firstCommands[3].handle + " 5:eeee..7:ffff",
	}, "\n") + "\n"
	result, err := transform.translateRecovery("call-2", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.translationError != "" || calls != 2 || evaluated != want {
		t.Fatalf("recovery = %+v, translations %d, evaluated %q, want %q", result, calls, evaluated, want)
	}
}

func TestMekugiFailedRecoveryPreservesEvaluatedBaseline(t *testing.T) {
	base := "in file.txt\ntype 1:aaaa \"value\"\n"
	want := "in file.txt\ntype 2:bbbb \"value\"\n"
	calls := 0
	var evaluated string
	transform, _, _, _ := newMekugiTestTransform(t, mekugiResultTranslatorFunc(func(_ context.Context, _ string, script string) (mekugiTranslationResult, error) {
		calls++
		if calls == 1 {
			return mekugiTranslationResult{
				diagnostic: "type: command 2, reason row-stale: rejected\n",
				rejections: []mekugi.HostRejection{{Command: 2, SourceLine: 2, Operation: "type", Target: "line", Reason: "row-stale"}},
			}, errors.New("rejected")
		}
		evaluated = script
		return mekugiTranslationResult{patch: []byte(testTranslatedPatch)}, nil
	}))
	if _, err := transform.translate("call-1", base, nil); err != nil {
		t.Fatal(err)
	}
	staleHandle := "C2:" + strings.Repeat("f", 64)
	failed, err := transform.translateRecovery("call-2", staleHandle+" 2:bbbb\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !failed.unevaluated || failed.correlationID != "call-1" || failed.attempt != 2 ||
		!strings.Contains(failed.translationError, `command handle "`+staleHandle+`" is stale`) || calls != 1 {
		t.Fatalf("failed recovery = %+v, translations %d", failed, calls)
	}
	payload := recoveryCommands(base)[1].handle + " 2:bbbb\n"
	recovered, err := transform.translateRecovery("call-3", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.translationError != "" || recovered.correlationID != "call-1" ||
		recovered.attempt != 3 || calls != 2 || evaluated != want {
		t.Fatalf("recovered = %+v, translations %d, evaluated %q", recovered, calls, evaluated)
	}
}

func TestMekugiUnchangedTargetRecoveryPreservesEvaluatedBaseline(t *testing.T) {
	base := "in file.txt\ntype 1:aaaa \"value\"\n"
	want := "in file.txt\ntype 2:bbbb \"value\"\n"
	calls := 0
	var evaluated string
	transform, _, _, _ := newMekugiTestTransform(t, mekugiResultTranslatorFunc(func(_ context.Context, _ string, script string) (mekugiTranslationResult, error) {
		calls++
		if calls == 1 {
			return mekugiTranslationResult{
				diagnostic: "type: command 2, reason row-stale: rejected\n",
				rejections: []mekugi.HostRejection{{Command: 2, SourceLine: 2, Operation: "type", Target: "line", Reason: "row-stale"}},
			}, errors.New("rejected")
		}
		evaluated = script
		return mekugiTranslationResult{patch: []byte(testTranslatedPatch)}, nil
	}))
	if _, err := transform.translate("call-1", base, nil); err != nil {
		t.Fatal(err)
	}
	command := recoveryCommands(base)[1]
	failed, err := transform.translateRecovery("call-2", command.handle+" 1:aaaa..1:aaaa\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !failed.unevaluated || failed.correlationID != "call-1" || failed.attempt != 2 ||
		!strings.Contains(failed.translationError, "replacement target must differ") || calls != 1 {
		t.Fatalf("unchanged recovery = %+v, translations %d", failed, calls)
	}
	recovered, err := transform.translateRecovery("call-3", command.handle+" 2:bbbb\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.translationError != "" || recovered.correlationID != "call-1" ||
		recovered.attempt != 3 || calls != 2 || evaluated != want {
		t.Fatalf("recovered = %+v, translations %d, evaluated %q", recovered, calls, evaluated)
	}
}

func TestMekugiRecoveryUsesLatestRejectedRecoveryInSameResponse(t *testing.T) {
	base := "in file.txt\ntype 1:aaaa \"value\"\n"
	firstRebuilt := "in file.txt\ntype 2:bbbb \"value\"\n"
	secondRebuilt := "in file.txt\ntype 3:cccc \"value\"\n"
	var evaluated []string
	transform, _, _, _ := newMekugiTestTransform(t, mekugiResultTranslatorFunc(func(_ context.Context, _ string, script string) (mekugiTranslationResult, error) {
		evaluated = append(evaluated, script)
		return mekugiTranslationResult{
			diagnostic: "type: command 2, reason row-stale: rejected\n",
			rejections: []mekugi.HostRejection{{Command: 2, SourceLine: 2, Operation: "type", Target: "line", Reason: "row-stale"}},
		}, errors.New("rejected")
	}))
	if _, err := transform.translate("call-1", base, nil); err != nil {
		t.Fatal(err)
	}
	baseCommand := recoveryCommands(base)[1]
	first, err := transform.translateRecovery("call-2", baseCommand.handle+" 2:bbbb\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	firstCommand := recoveryCommands(firstRebuilt)[1]
	second, err := transform.translateRecovery("call-3", firstCommand.handle+" 3:cccc\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluated) != 3 || evaluated[1] != firstRebuilt || evaluated[2] != secondRebuilt {
		t.Fatalf("evaluated scripts = %q", evaluated)
	}
	if first.correlationID != "call-1" || second.correlationID != first.correlationID ||
		first.attempt != 2 || second.attempt != 3 || !second.evaluatorRejected {
		t.Fatalf("recovery chain = %+v then %+v", first, second)
	}
}

func TestMekugiRetainedProxyRejectionAdvancesRecoveryAttempt(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{
		"call-1": {
			toolName: mekugiToolName, script: testMekugiScript, translationError: "rejected",
			evaluatorRejected: true, correlationID: "call-1", attempt: 1, sequence: 1,
		},
		"call-2": {
			toolName: mekugiToolName, script: `type 2:ffff "bad"` + "\n",
			translationError: "stale", unevaluated: true,
			correlationID: "call-1", attempt: 2, sequence: 2,
		},
	}); err != nil {
		t.Fatal(err)
	}
	base, err := proxy.recoverableHistory("session")
	if err != nil {
		t.Fatal(err)
	}
	if base.attempt != 1 {
		t.Fatalf("recoverable base attempt = %d, want 1", base.attempt)
	}
	if got := proxy.latestRecoveryAttempt("session", "call-1"); got != 2 {
		t.Fatalf("latest retained chain attempt = %d, want 2", got)
	}
}

func TestMekugiTranslationFailureReturnsImmediateDiagnosticExec(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("selector is not unique")
	}))
	originalItem := mustTestJSON(t, testMekugiItem())
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{"type": "message", "future": "preserved", "metadata": map[string]any{"name": "apply_patch"}},
			json.RawMessage(originalItem),
		},
		"future": map[string]any{"kept": true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status string                       `json:"status"`
		Error  json.RawMessage              `json:"error"`
		Output []map[string]json.RawMessage `json:"output"`
		Future json.RawMessage              `json:"future"`
	}
	if err := json.Unmarshal(visible, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "completed" || len(response.Error) != 0 || len(response.Output) != 2 {
		t.Fatalf("translation rejection carrier = %s", visible)
	}
	carrier := response.Output[1]
	history, remembered := proxy.history(transform.historySessionID, "call-H")
	if !remembered {
		t.Fatal("translation rejection carrier was not remembered")
	}
	if jsonString(carrier, "name") != "exec" || jsonString(carrier, "input") != history.carrierInput() || jsonString(carrier, "call_id") != "call-H" || !strings.Contains(history.carrierInput(), "selector is not unique") {
		t.Fatalf("diagnostic exec carrier = %s", visible)
	}
	if jsonString(response.Output[0], "future") != "preserved" || string(response.Future) != `{"kept":true}` || !bytes.Contains(response.Output[0]["metadata"], []byte(`"name":"apply_patch"`)) {
		t.Fatalf("unrelated response data changed: %s", visible)
	}

	replay, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"input": []any{
			testCodeModeAdditionalTools(testCodeModeDescription),
			carrier,
			map[string]any{"type": "custom_tool_call_output", "call_id": "call-H", "output": history.translationError, "future": true},
			map[string]any{"type": "custom_tool_call_output", "call_id": "other", "output": "keep"},
		},
		"tools": []any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}
	continuation, err := proxy.prepareRequest(t.Context(), &replay, "session-1", "thread-1", metadata, true)
	if err != nil || continuation == nil {
		t.Fatalf("prepare rejection continuation = transform %v, error %v", continuation, err)
	}
	defer continuation.Close()
	var replayed []map[string]json.RawMessage
	if err := json.Unmarshal(replay.fields["input"], &replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 4 || jsonString(replayed[1], "name") != mekugiToolName || jsonString(replayed[1], "input") != testMekugiScript || string(replayed[2]["future"]) != "true" || jsonString(replayed[2], "output") != history.translationError || jsonString(replayed[3], "output") != "keep" {
		t.Fatalf("restored mekugi rejection = %s", replay.fields["input"])
	}
}

func TestMekugiStreamingTranslationFailureCompletesDiagnosticExecLifecycle(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("parent directory does not exist")
	}))
	item := testMekugiItem()
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""
	completed := map[string]any{
		"status":      "completed",
		"output":      []any{item},
		"future":      map[string]any{"kept": true},
		"tools":       []any{map[string]any{"type": "custom", "name": mekugiToolName}},
		"tool_choice": mekugiToolName,
	}
	body := "event: response.output_item.added\n" +
		"data: " + string(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})) + "\n\n" +
		"event: response.custom_tool_call_input.done\n" +
		"data: " + string(mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testMekugiScript})) + "\n\n" +
		"event: response.future\n" +
		"data: " + string(mustTestJSON(t, map[string]any{"type": "response.future", "future": "future-preserved"})) + "\n\n" +
		"event: response.output_item.done\n" +
		"data: " + string(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})) + "\n\n" +
		"event: response.completed\n" +
		"data: " + string(mustTestJSON(t, map[string]any{"type": "response.completed", "response": completed, "future": "outer-preserved"})) + "\n\n"
	var output bytes.Buffer
	terminal, err := copySSETransformed(&output, strings.NewReader(body), transform, nil)
	if err != nil || terminal != responseTerminalCompleted {
		t.Fatalf("translation rejection stream = terminal %v, error %v, output %q", terminal, err, output.String())
	}
	visible := output.String()
	for _, required := range []string{"event: response.output_item.added", "event: response.custom_tool_call_input.done", "event: response.output_item.done", "event: response.completed", `"name":"exec"`, "text(\\\"parent directory does not exist", `"future":{"kept":true}`, `"future":"outer-preserved"`, "future-preserved", `"name":"lookup"`, `"tool_choice":"auto"`} {
		if !strings.Contains(visible, required) {
			t.Fatalf("translation carrier missing %q: %q", required, visible)
		}
	}
	for _, forbidden := range []string{"response.failed", "stream disconnected"} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("translation carrier exposed %q: %q", forbidden, visible)
		}
	}
	if _, remembered := proxy.history(transform.historySessionID, "call-H"); !remembered {
		t.Fatal("streaming translation rejection carrier was not remembered")
	}
}

func TestMekugiTerminalProjectionRestoresCompletedCallsOnly(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, status := range []string{"failed", "incomplete"} {
			for _, transport := range []string{"json", "sse", "sse-after-item", "sse-after-incomplete-item", "sse-no-status"} {
				t.Run(fmt.Sprintf("native=%v/%s/%s", native, status, transport), func(t *testing.T) {
					calls := 0
					proxy := newManagedMekugiProxy(t, testTranslator(t, &calls))
					var transform *mekugiResponseTransform
					if native {
						transform, _ = newNativeMekugiTestTransformWithProxy(t, proxy)
					} else {
						transform, _, _, _ = newMekugiTestTransformWithProxy(t, proxy)
					}
					completed := testMekugiItem()
					unfinished := testMekugiItem()
					unfinished["id"], unfinished["call_id"], unfinished["status"] = "item-unfinished", "call-unfinished", "in_progress"
					// Even syntactically complete input is not executable before completion.
					if transport == "sse-after-item" {
						if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": completed})); err != nil {
							t.Fatal(err)
						}
						if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": unfinished})); err != nil {
							t.Fatal(err)
						}
					}
					if transport == "sse-after-incomplete-item" {
						unfinished["status"] = "incomplete"
						if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": unfinished})); err != nil {
							t.Fatal(err)
						}
					}
					responseFields := map[string]any{
						"status": status, "output": []any{completed, unfinished},
						"tools":       []any{map[string]any{"type": "custom", "name": mekugiToolName}},
						"tool_choice": mekugiToolName, "future": map[string]any{"kept": true},
					}
					wireStatus := status
					if transport == "sse-no-status" {
						delete(responseFields, "status")
						wireStatus = ""
					}
					response := mustTestJSON(t, responseFields)
					var visible []byte
					var err error
					if transport == "json" {
						visible, err = transform.TransformJSON(response)
					} else {
						var events [][]byte
						events, err = transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response." + status, "response": json.RawMessage(response)}))
						if err == nil {
							var envelope map[string]json.RawMessage
							if len(events) == 0 || json.Unmarshal(events[len(events)-1], &envelope) != nil || jsonString(envelope, "type") != "response."+status {
								t.Fatalf("terminal events = %s", events)
							}
							visible = envelope["response"]
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					var result map[string]json.RawMessage
					var output []map[string]json.RawMessage
					if json.Unmarshal(visible, &result) != nil || json.Unmarshal(result["output"], &output) != nil || len(output) != 2 {
						t.Fatalf("terminal response = %s", visible)
					}
					if jsonString(result, "status") != wireStatus || jsonString(result, "tool_choice") != "auto" || !bytes.Equal(result["tools"], transform.originalTools) || string(result["future"]) != `{"kept":true}` {
						t.Fatalf("terminal contract = %s", visible)
					}
					history, remembered := proxy.history(transform.historySessionID, "call-H")
					payloadField := "input"
					if native {
						payloadField = "arguments"
					}
					if calls != 1 || !remembered || jsonString(output[0], "name") != history.carrierName || jsonString(output[0], payloadField) != history.carrierInput() {
						t.Fatalf("completed carrier: calls=%d remembered=%v output=%s", calls, remembered, visible)
					}
					if jsonString(output[1], "status") != unfinished["status"] || jsonString(output[1], "input") != testMekugiScript {
						t.Fatalf("unfinished call changed = %s", visible)
					}
					if _, remembered := proxy.history(transform.historySessionID, "call-unfinished"); remembered {
						t.Fatal("unfinished call entered replay history")
					}
					if err := transform.Finish(transport != "json"); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestMekugiOutputItemDoneRespectsIncompleteStatus(t *testing.T) {
	for _, status := range []string{"", "completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, &calls))
			item := testMekugiItem()
			if status == "" {
				delete(item, "status")
			} else {
				item["status"] = status
			}
			events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item}))
			if err != nil || len(events) != 1 {
				t.Fatalf("item completion = %s, %v", events, err)
			}
			wantCalls := 1
			if status == "incomplete" {
				wantCalls = 0
			}
			_, remembered := proxy.history(transform.historySessionID, "call-H")
			if calls != wantCalls || remembered != (wantCalls == 1) {
				t.Fatalf("calls = %d, remembered = %v", calls, remembered)
			}
		})
	}
}

func TestMekugiStreamingTranslationFailureRejectsMalformedTerminal(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("selector is not unique")
	}))
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""
	if visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil || visible != nil {
		t.Fatalf("buffer mekugi call = %q, error %v", visible, err)
	}
	if visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testMekugiScript})); err != nil || len(visible) != 2 || !bytes.Contains(visible[1], []byte("selector is not unique")) {
		t.Fatalf("release mekugi rejection carrier = %q, error %v", visible, err)
	}
	if visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": testMekugiItem()})); err != nil || len(visible) != 1 {
		t.Fatalf("complete mekugi rejection carrier = %q, error %v", visible, err)
	}
	visible, err := transform.TransformSSE([]byte(`{"type":"response.completed","response":null}`))
	if err == nil || visible != nil || !strings.Contains(err.Error(), "decode mekugi-enabled response") {
		t.Fatalf("malformed terminal = %q, error %v", visible, err)
	}
}

func TestMekugiMalformedCallStillFailsRequest(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	item := testMekugiItem()
	item["type"] = "message"
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{item}}))
	if err == nil || visible != nil {
		t.Fatalf("malformed mekugi call = visible %q, error %v", visible, err)
	}
	if _, remembered := proxy.history(transform.historySessionID, "call-H"); remembered {
		t.Fatal("malformed mekugi call created history")
	}
}

func TestMekugiTranslationCancellationRemainsRequestCancellation(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(ctx context.Context, _ string, _ string) ([]byte, error) {
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	transform.ctx = ctx
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{testMekugiItem()}}))
	if !errors.Is(err, context.Canceled) || visible != nil {
		t.Fatalf("canceled translation = visible %q, error %v", visible, err)
	}
	if _, remembered := proxy.history(transform.historySessionID, "call-H"); remembered {
		t.Fatal("canceled translation created rejection history")
	}
}

func TestMekugiHistoryByteAccountingIncludesExistingCalls(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{
		"call-1": {script: "first"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.rememberBatch("session", map[string]mekugiHistory{
		"call-2": {script: "second"},
	}); err != nil {
		t.Fatal(err)
	}

	session := proxy.sessions["session"]
	want := 0
	for _, history := range session.calls {
		want += history.bytes
	}
	if session.bytes != want || proxy.historyBytes != want {
		t.Fatalf("history bytes = session %d, global %d, want %d", session.bytes, proxy.historyBytes, want)
	}
}

func TestMekugiHistoryDoesNotEvictActiveSessions(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	for index := range maxSessionHistories {
		sessionID := fmt.Sprintf("session-%03d", index)
		if err := proxy.rememberBatch(sessionID, map[string]mekugiHistory{"call": {script: sessionID}}); err != nil {
			t.Fatalf("remember session %d: %v", index, err)
		}
	}
	if err := proxy.activateSession("session-000"); err != nil {
		t.Fatal(err)
	}
	defer proxy.deactivateSession("session-000")

	if err := proxy.rememberBatch("session-new", map[string]mekugiHistory{"call": {script: "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := proxy.sessions["session-000"]; !ok {
		t.Fatal("active oldest session was evicted")
	}
	if _, ok := proxy.sessions["session-001"]; ok {
		t.Fatal("oldest inactive session was not evicted")
	}
}

func TestMekugiHistoryEvictsOldestCallsAndSessions(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	for index := range maxSessionTurns + 1 {
		callID := fmt.Sprintf("call-%03d", index)
		err := proxy.rememberBatch("session", map[string]mekugiHistory{
			callID: {
				toolName:          mekugiToolName,
				script:            callID,
				translationError:  "rejected",
				evaluatorRejected: true,
			},
		})
		if err != nil {
			t.Fatalf("remember call %d: %v", index, err)
		}
	}
	session := proxy.sessions["session"]
	if len(session.calls) != maxSessionTurns {
		t.Fatalf("retained calls = %d, want %d", len(session.calls), maxSessionTurns)
	}
	if _, ok := session.calls["call-000"]; ok {
		t.Fatal("oldest call was not evicted")
	}
	if _, ok := session.calls[fmt.Sprintf("call-%03d", maxSessionTurns)]; !ok {
		t.Fatal("newest call was not retained")
	}
	latest, err := proxy.recoverableHistory("session")
	if err != nil || latest.script != fmt.Sprintf("call-%03d", maxSessionTurns) {
		t.Fatalf("latest history = %+v, error %v", latest, err)
	}

	for index := range maxSessionHistories {
		sessionID := fmt.Sprintf("session-%03d", index)
		if err := proxy.rememberBatch(sessionID, map[string]mekugiHistory{"call": {script: sessionID}}); err != nil {
			t.Fatalf("remember session %d: %v", index, err)
		}
	}
	if err := proxy.rememberBatch("session-new", map[string]mekugiHistory{"call": {script: "new"}}); err != nil {
		t.Fatalf("remember replacement session: %v", err)
	}
	if len(proxy.sessions) != maxSessionHistories {
		t.Fatalf("retained sessions = %d, want %d", len(proxy.sessions), maxSessionHistories)
	}
	if _, ok := proxy.sessions["session-000"]; ok {
		t.Fatal("oldest session was not evicted")
	}
	if _, ok := proxy.sessions["session-new"]; !ok {
		t.Fatal("new session was not retained")
	}
}

func TestMekugiBoundsTranslationAndHistory(t *testing.T) {
	t.Run("translation output capacity", func(t *testing.T) {
		transform, proxy, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
			return make([]byte, maxMekugiPatchBytes+1), nil
		}))
		payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{testMekugiItem()}})
		visible, err := transform.TransformJSON(payload)
		if err == nil || visible != nil || !strings.Contains(err.Error(), "translation exceeds") {
			t.Fatalf("oversized translation = visible %q, error %v", visible, err)
		}
		if _, remembered := proxy.history(transform.historySessionID, "call-H"); remembered {
			t.Fatal("oversized translation created rejection history")
		}
	})

	t.Run("script capacity", func(t *testing.T) {
		transform, proxy, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
			t.Fatal("translator called for oversized script")
			return nil, nil
		}))
		item := testMekugiItem()
		item["input"] = strings.Repeat("x", maxMekugiScriptBytes+1)
		visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{item}}))
		if err == nil || visible != nil || !strings.Contains(err.Error(), "script exceeds") {
			t.Fatalf("oversized script = visible bytes %d, error %v", len(visible), err)
		}
		if _, remembered := proxy.history(transform.historySessionID, "call-H"); remembered {
			t.Fatal("oversized script created rejection history")
		}
	})

	t.Run("translator capacity", func(t *testing.T) {
		transform, proxy, _, _ := newMekugiTestTransform(t, mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
			return nil, fmt.Errorf("%w: diagnostic overflow", errMekugiCapacity)
		}))
		visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{testMekugiItem()}}))
		if !errors.Is(err, errMekugiCapacity) || visible != nil {
			t.Fatalf("translator capacity = visible %q, error %v", visible, err)
		}
		if _, remembered := proxy.history(transform.historySessionID, "call-H"); remembered {
			t.Fatal("translator capacity created rejection history")
		}
	})

	t.Run("global history", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
		proxy.historyBytes = maxMekugiHistoryGlobalBytes
		if err := proxy.rememberBatch("session", map[string]mekugiHistory{"call": {script: "x", patch: "y"}}); err == nil {
			t.Fatal("history exceeded global capacity")
		}
		if len(proxy.sessions) != 0 {
			t.Fatalf("failed history reservation created %d sessions", len(proxy.sessions))
		}
	})

	t.Run("batch history is atomic", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
		proxy.historyBytes = maxMekugiHistoryGlobalBytes - 1
		err := proxy.rememberBatch("session", map[string]mekugiHistory{
			"call-first":  {script: "x", patch: "y"},
			"call-second": {script: "x", patch: "y"},
		})
		if err == nil {
			t.Fatal("history batch exceeded global capacity")
		}
		if len(proxy.sessions) != 0 {
			t.Fatalf("failed history batch created %d sessions", len(proxy.sessions))
		}
	})
}

func TestMekugiBoundsPendingStreamCallsAndRejectsRelatedFutureEvents(t *testing.T) {
	t.Run("duplicate item", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		added := testMekugiItem()
		added["status"] = "in_progress"
		added["input"] = ""
		payload := mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})
		if _, err := transform.TransformSSE(payload); err != nil {
			t.Fatal(err)
		}
		if visible, err := transform.TransformSSE(payload); err == nil || visible != nil {
			t.Fatalf("duplicate item = visible %q, error %v", visible, err)
		}
	})

	t.Run("pending count", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		for index := range maxMekugiPendingCalls + 1 {
			added := testMekugiItem()
			added["status"] = "in_progress"
			added["input"] = ""
			added["id"] = fmt.Sprintf("item-%d", index)
			added["call_id"] = fmt.Sprintf("call-%d", index)
			visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added}))
			if index < maxMekugiPendingCalls {
				if err != nil || visible != nil {
					t.Fatalf("pending item %d = visible %q, error %v", index, visible, err)
				}
				continue
			}
			if err == nil || visible != nil {
				t.Fatalf("excess pending item = visible %q, error %v", visible, err)
			}
		}
	})

	t.Run("malformed pending event", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		added := testMekugiItem()
		added["status"] = "in_progress"
		added["input"] = ""
		if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil {
			t.Fatal(err)
		}
		malformed := []byte(`{"type":1,"item_id":"item-H","delta":"secret script"}`)
		if visible, err := transform.TransformSSE(malformed); err == nil || visible != nil {
			t.Fatalf("malformed pending event = visible %q, error %v", visible, err)
		}
	})

	t.Run("malformed unrelated event", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		malformed := []byte(`{"type":1,"future":"kept"}`)
		visible, err := transform.TransformSSE(malformed)
		if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], malformed) {
			t.Fatalf("malformed unrelated event = visible %q, error %v", visible, err)
		}
	})

	t.Run("incomplete terminal event", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		added := testMekugiItem()
		added["status"] = "in_progress"
		added["input"] = ""
		if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil {
			t.Fatal(err)
		}
		completed := mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{}}})
		if visible, err := transform.TransformSSE(completed); err == nil || visible != nil {
			t.Fatalf("incomplete terminal event = visible %q, error %v", visible, err)
		}
	})

	t.Run("related future event", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		added := testMekugiItem()
		added["status"] = "in_progress"
		added["input"] = ""
		if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil {
			t.Fatal(err)
		}
		future := mustTestJSON(t, map[string]any{"type": "response.future", "call_id": "call-H", "input": "secret"})
		if visible, err := transform.TransformSSE(future); err == nil || visible != nil {
			t.Fatalf("related future event = visible %q, error %v", visible, err)
		}
	})

	t.Run("unrelated future event", func(t *testing.T) {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		future := mustTestJSON(t, map[string]any{"type": "response.future", "call_id": "other", "name": "other"})
		visible, err := transform.TransformSSE(future)
		if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], future) {
			t.Fatalf("unrelated future event = visible %q, error %v", visible, err)
		}
	})
}

func TestInProcessMekugiToolDescription(t *testing.T) {
	translator := newInProcessMekugiTranslator(t.TempDir())
	if got, want := translator.ToolDescription(), mekugi.ToolDescription(); got != want {
		t.Fatalf("installed tool description differs from authoritative description:\n got %q\nwant %q", got, want)
	}
}

func TestInProcessMekugiTranslatorUsesBaseDirectoryWithoutConfinement(t *testing.T) {
	translator := newInProcessMekugiTranslator(t.TempDir())
	parent := t.TempDir()
	directory := filepath.Join(parent, "base")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "existing.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	translated, err := translator.Translate(t.Context(), directory, "in existing.txt\nadd 2:1636 \"inserted\\n\"\ntype 3:b1e9 \"THIRD\"\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"*** Update File: existing.txt", "+inserted", "+THIRD"} {
		if !bytes.Contains(translated.patch, []byte(required)) {
			t.Fatalf("translation does not contain %q: %s", required, translated.patch)
		}
	}
	if !strings.HasPrefix(translated.report, "in existing.txt\n") {
		t.Fatalf("translation report = %q", translated.report)
	}
	for _, required := range []string{
		"refs 2 add existing.txt\n",
		"refs 3 type existing.txt\n",
		"2:e8dd inserted\n",
		"4:1186 THIRD\n",
	} {
		if !strings.Contains(translated.report, required) {
			t.Fatalf("translation report %q lacks %q", translated.report, required)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\nsecond\nthird\n" {
		t.Fatalf("translation mutated file to %q", content)
	}

	conflict := "in existing.txt\ntype 1:a793..2:1636 \"replacement\"\ntype 2:1636..3:b1e9 \"overlap\"\n"
	if translated, err := translator.Translate(t.Context(), directory, conflict); err == nil || !strings.Contains(translated.diagnostic, "conflicts with edit") {
		t.Fatalf("overlapping baseline translation error = %v", err)
	}

	outsidePath := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"../outside.txt", outsidePath} {
		translated, err := translator.Translate(t.Context(), directory, "in "+target+"\ntype 1:3120 \"changed\"\n")
		if err != nil {
			t.Fatalf("translate %q: %v", target, err)
		}
		wantPath := filepath.Clean(target)
		if !bytes.Contains(translated.patch, []byte("*** Update File: "+wantPath)) {
			t.Fatalf("translation for %q does not contain cleaned target %q: %s", target, wantPath, translated.patch)
		}
	}
}

func mustTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jsonQuoted(value string) string {
	return strconv.Quote(value)
}

func TestMekugiReplacementSupportsTopLevelCodeModeForGrok(t *testing.T) {
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": grokModel, "stream": true, "input": []any{map[string]any{"role": "user", "content": "probe"}},
		"tools": []any{map[string]any{"type": "custom", "name": "exec", "description": testCodeModeDescription}}, "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, err := proxy.prepareRequest(t.Context(), &request, "grok-session", "grok-thread", codexTurnMetadata{RequestKind: "turn", SubagentKind: "thread_spawn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	if transform.nativeTools || transform.codeModeToolName != "exec" {
		t.Fatal("top-level Code Mode became a native shell carrier")
	}
	var tools []map[string]json.RawMessage
	json.Unmarshal(request.fields["tools"], &tools)
	var execDescription string
	names := map[string]bool{}
	for _, tool := range tools {
		names[jsonString(tool, "name")] = true
		if jsonString(tool, "name") == "exec" {
			execDescription = jsonString(tool, "description")
		}
	}
	if !names["hpatch"] || !names["shell"] || strings.Contains(execDescription, codeModeApplyPatchHeading) || strings.Contains(execDescription, "tools.exec_command") {
		t.Fatal("Code Mode projection did not expose mekugi and shell")
	}
	if _, err := translateGrokRequest(mustTestJSON(t, request.fields)); err != nil {
		t.Fatal(err)
	}
}

func TestShellRecoversCodeModePrograms(t *testing.T) {
	const legacy = "const result = await tools.exec_command({\"cmd\":\"git status --short\",\"login\":false,\"max_output_tokens\":24000});\n" +
		"text(JSON.stringify(Object.assign({}, result, {\"retained\":false})));"
	tests := []struct {
		name  string
		input string
	}{
		{name: "original standalone helper", input: `text("I cannot send collaboration tool from exec")`},
		{name: "original catalog lookup", input: `const hits = ALL_TOOLS.filter(x => /send_message|collaboration/.test(x.name+" "+x.description)); text(hits);`},
		{name: "commented nested call", input: "// Code Mode\ntext(await tools.exec({}));"},
		{
			name:  "legacy exec_command carrier",
			input: legacy,
		},
		{
			name:  "exec program",
			input: "const r = await tools.exec({command:\"git show --stat\",workdir:\"/workspace\"});\ntext(r);",
		},
		{
			name:  "write_stdin program",
			input: "const r = await tools.write_stdin({chars:\"\",session_id:83733,yield_time_ms:30000});\ntext(r.output);",
		},
		{
			name:  "same-line exec projection",
			input: "const r = await tools.exec({command:\"printf ok\"}); text(r);",
		},
		{
			name:  "same-line write_stdin projection",
			input: "const r = await tools.write_stdin({chars:\"\",session_id:83733}); text(r.output);",
		},
		{
			name: "multiple tool calls remain unchanged",
			input: "const r = await tools.exec({command:\"git show --stat\"});\n" +
				"text(r);\nconst s = await tools.write_stdin({chars:\"\",session_id:83733});\ntext(r.output);",
		},
		{
			name:  "leading whitespace",
			input: "\n \tconst r = await tools.exec({command:\"printf ok\"});\ntext(r);",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recovered := shellCodeModeRecovery(
				toolContribution{PluginID: "builtin.shell", Name: "shell"},
				test.input,
			)
			if !recovered {
				t.Fatal("Code Mode program was not recovered")
			}
			warningInput := misuseWarningProjection(shellCodeModeRecoveryWarning)
			if strings.Contains(test.input, "tools.exec_command") {
				warningInput += misuseWarningProjection(nativeExecCommandWarning)
			}
			offset := inspectCodeModeRuntime(test.input).warningOffset
			want := test.input[:offset] + warningInput + test.input[offset:]

			translator := mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
				return []byte(testTranslatedPatch), nil
			})
			transform, proxy, _, _ := newMekugiTestTransform(t, translator)
			visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"status": "completed",
				"output": []any{map[string]any{
					"type": "custom_tool_call", "id": "item-shell", "call_id": "call-shell",
					"name": "shell", "input": test.input, "status": "completed",
				}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(visible, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 1 || jsonString(response.Output[0], "name") != "exec" ||
				jsonString(response.Output[0], "input") != want {
				t.Fatalf("recovered shell carrier = %s", visible)
			}
			history, ok := proxy.history(transform.historySessionID, "call-shell")
			if !ok || history.toolName != "shell" || history.carrierPayload != want || !history.replayCarrier {
				t.Fatalf("recovered shell history = %+v, available %t", history, ok)
			}

			replay, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
				"input": []any{
					response.Output[0],
					map[string]any{
						"type": "custom_tool_call_output", "call_id": "call-shell", "output": "command output",
					},
				},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
				t.Fatal(err)
			}
			firstReplay := bytes.Clone(replay.fields["input"])
			if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(replay.fields["input"], firstReplay) {
				t.Fatalf("recovered carrier replay was not idempotent: %s", replay.fields["input"])
			}
			var replayed []map[string]json.RawMessage
			if err := json.Unmarshal(replay.fields["input"], &replayed); err != nil {
				t.Fatal(err)
			}
			if len(replayed) != 2 || jsonString(replayed[0], "name") != "exec" ||
				jsonString(replayed[0], "input") != want ||
				jsonString(replayed[1], "output") != "command output" {
				t.Fatalf("recovered carrier replay = %s", replay.fields["input"])
			}
		})
	}
}

func TestShellCodeModeRecoveryRejectsNearMisses(t *testing.T) {
	contribution := toolContribution{PluginID: "builtin.shell", Name: "shell"}
	valid := "const r = await tools.exec({command:\"printf ok\"});\ntext(r);"
	for _, input := range []string{
		"printf ok",
		"#!node\n" + valid,
		"#!params={}\n" + valid,
		"# const r = await tools.exec({command:\"printf ok\"});\ntext(r);",
		"printf '%s' 'const r = await tools.exec({command:\"printf ok\"}); text(r);'",
	} {
		if shellCodeModeRecovery(contribution, input) {
			t.Errorf("near-miss shell input recovered: %q", input)
		}
	}
	if shellCodeModeRecovery(
		toolContribution{PluginID: "configured", Name: "shell"},
		valid,
	) {
		t.Error("configured shell plugin received Luna recovery")
	}
}
