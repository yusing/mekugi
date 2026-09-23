package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const testCodeModeDescription = "Run JavaScript. All nested tools are available on the global `tools` object, including `tools.exec_command` and `tools.apply_patch`.\n\n### `exec_command`\nRun a command.\n\nexec tool declaration:\n```ts\ndeclare const tools: { exec_command(args: { cmd: string }): Promise<unknown>; };\n```\n\n### `apply_patch`\nApply a patch.\n\nexec tool declaration:\n```ts\ndeclare const tools: { apply_patch(input: string): Promise<unknown>; };\n```"
const testBaseInstructions = "caller-owned base instructions\n"

const testTranslatedPatch = "*** Begin Patch\n*** Add File: created.txt\n+payload\n*** End Patch\n"
const testMekugiScript = testTranslatedPatch
const testMekugiReport = "Success. Updated the following files:\nA created.txt\n"

var testShellEditSource = "await tools.apply_patch(" + strconv.Quote(testTranslatedPatch) + ");"

const testToolPluginDeclaration = `export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "proxy.test",
  tools: [{
    specification: {type: "custom", name: "plugin_tool", description: "fixture plugin tool"},
    parse(input) { return input; },
    argv(parsed) { return [parsed]; },
    execute(argv) { return {stdout: argv.join("|"), stderr: "", exitCode: 0}; }
  }]
};`

func mustTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testCodeModeAdditionalTools(description string) map[string]any {
	return map[string]any{
		"type": "additional_tools", "role": "developer",
		"tools": []any{
			map[string]any{"type": "namespace", "name": "functions", "tools": []any{
				map[string]any{"type": "custom", "name": "exec", "description": description, "format": map[string]any{"type": "text"}},
				map[string]any{"type": "function", "name": "wait"},
			}},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "send_message"},
			}},
		},
	}
}

func testFlatCodeModeAdditionalTools(description string) map[string]any {
	return map[string]any{
		"type": "additional_tools", "role": "developer",
		"tools": []any{
			map[string]any{"type": "custom", "name": "exec", "description": description, "format": map[string]any{"type": "text"}},
			map[string]any{"type": "function", "name": "wait"},
		},
	}
}

func testNativeResponsesTools() []any {
	return []any{
		map[string]any{"type": "function", "name": nativeExecCommandToolName, "description": "run a command"},
		map[string]any{"type": "custom", "name": applyPatchToolName, "description": "apply a patch"},
	}
}

func newManagedMekugiProxy(t *testing.T) *mekugiProxy {
	t.Helper()
	return newProxyWithSharedTestRegistry(t, sharedProxyTestRegistry(t))
}

func (p *mekugiProxy) prepareRequest(ctx context.Context, request *parsedResponsesRequest, sessionID, threadID string, metadata codexTurnMetadata, metadataValid bool) (*mekugiResponseTransform, error) {
	return p.prepareModelRequest(ctx, request, sessionID, threadID, metadata, metadataValid, false)
}

func attachTestReplayStore(t *testing.T, proxy *mekugiProxy) {
	t.Helper()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
}

func newToolPluginTestProxy(t *testing.T) *mekugiProxy {
	t.Helper()
	return newProxyWithSharedTestRegistry(t, pluginProxyTestFixture.get(t, testToolPluginDeclaration))
}

func newMekugiTestTransform(t *testing.T) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest, string) {
	t.Helper()
	return newMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
}

func newMekugiTestTransformWithProxy(t *testing.T, proxy *mekugiProxy) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest, string) {
	t.Helper()
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": "task"}}, "tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, proxy, &request, workspace
}

func newNativeMekugiTestTransformWithProxy(t *testing.T, proxy *mekugiProxy) (*mekugiResponseTransform, *parsedResponsesRequest) {
	t.Helper()
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{map[string]any{"role": "user", "content": "task"}},
		"tools": testNativeResponsesTools(), "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "native-session", "native-thread", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, &request
}

func testMekugiItem() map[string]any {
	return map[string]any{
		"type": "custom_tool_call", "id": "item-H", "call_id": "call-H",
		"name": "exec", "input": testShellEditSource, "status": "completed",
	}
}

func shellCommentaryTestItem() map[string]any {
	return map[string]any{
		"type": "custom_tool_call", "id": "item-runtime", "call_id": "call-runtime",
		"name": "exec", "input": `await journal({op: "add", text: "Working"});`, "status": "completed",
	}
}

func newRuntimeCommentaryTransform(t *testing.T) (*mekugiResponseTransform, *mekugiProxy) {
	t.Helper()
	transform, proxy, _, _ := newMekugiTestTransform(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	return transform, proxy
}

func runtimeCommentaryToken(t *testing.T, transform *mekugiResponseTransform) string {
	t.Helper()
	if len(transform.commentarySubscriptions) != 1 {
		t.Fatalf("commentary subscription count = %d", len(transform.commentarySubscriptions))
	}
	return transform.commentarySubscriptions[0].token
}

func titleRequestFields() map[string]any {
	return map[string]any{
		"model": "gpt-test", "instructions": "Generate a short title.",
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "Format Run previews"}},
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "title", "strict": true,
			"schema": map[string]any{"type": "object", "properties": map[string]any{
				"title": map[string]any{"type": "string"},
			}, "required": []string{"title"}, "additionalProperties": false},
		}},
	}
}

type shellWorkerTestInvocation struct {
	directory   string
	environment []string
}

func newShellWorkerTestInvocation(directory string, environment ...string) shellWorkerTestInvocation {
	base := append(os.Environ(), "CODEX_THREAD_ID=")
	return shellWorkerTestInvocation{directory: directory, environment: append(base, environment...)}
}

func runShellWorkerTest(
	t *testing.T,
	registry *toolRegistry,
	interpreter string,
	interpreterArguments []string,
	script string,
	stdin *os.File,
	invocations ...shellWorkerTestInvocation,
) (stdout, stderr string, exitCode int) {
	t.Helper()
	if len(invocations) > 1 {
		t.Fatal("runShellWorkerTest accepts at most one invocation")
	}
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "CODEX_THREAD_ID=", routerTestWorkerEnvironment+"=1")
	if len(invocations) == 1 {
		directory = invocations[0].directory
		environment = append(slices.Clone(invocations[0].environment), routerTestWorkerEnvironment+"=1")
	}
	environment = prependToolFrontendPath(environment, registry.frontendDirectory)
	arguments := append(append([]string{}, interpreterArguments...), "-c", script)
	command := exec.CommandContext(t.Context(), interpreter, arguments...)
	command.Dir, command.Env, command.Stdin = directory, environment, stdin
	var out, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostic
	runErr := command.Run()
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			t.Fatal(runErr)
		}
		exitCode = mrunProcessExitCode(exit)
	}
	return out.String(), diagnostic.String(), exitCode
}

func prependToolFrontendPath(environment []string, frontendDirectory string) []string {
	result := make([]string, 0, len(environment)+1)
	path := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
			continue
		}
		result = append(result, entry)
	}
	return append(result, "PATH="+frontendDirectory+string(os.PathListSeparator)+path)
}

func bindTestHandleScope(t *testing.T, store *mekugiReplayStore, ctx context.Context, parent, fork string) context.Context {
	t.Helper()
	thread := store.scoped(ctx).session.Thread
	metadata := codexTurnMetadata{ThreadID: thread, ParentThreadID: parent, ForkedFromThreadID: fork}
	if parent != "" {
		metadata.SubagentKind = "thread_spawn"
	}
	ctx, err := store.prepareHandleScope(ctx, metadata)
	if err == nil {
		err = store.retainInput(ctx, "/w", nil, nil, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}
