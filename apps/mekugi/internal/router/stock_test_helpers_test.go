package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

const testCodeModeDescription = "Run JavaScript. All nested tools are available on the global `tools` object, including `tools.exec_command` and `tools.apply_patch`.\n\n### `exec_command`\nRun a command.\n\nexec tool declaration:\n```ts\ndeclare const tools: { exec_command(args: { cmd: string }): Promise<unknown>; };\n```\n\n### `apply_patch`\nApply a patch.\n\nexec tool declaration:\n```ts\ndeclare const tools: { apply_patch(input: string): Promise<unknown>; };\n```"
const testBaseInstructions = "caller-owned base instructions\n"

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

func testExecResponsesTools() []any {
	return []any{
		map[string]any{"type": "custom", "name": "exec", "description": testCodeModeDescription},
	}
}

func newManagedMekugiProxy(t *testing.T) *mekugiProxy {
	t.Helper()
	return newProxyWithSharedTestRegistry(t, sharedProxyTestRegistry(t))
}

func (p *mekugiProxy) prepareRequest(ctx context.Context, request *codexresponses.Request, sessionID, threadID string, metadata codexresponses.TurnMetadata, metadataValid bool) (*mekugiResponseTransform, error) {
	return p.prepareModelRequest(ctx, request, sessionID, threadID, metadata, metadataValid, false)
}

func attachTestReplayStore(t *testing.T, proxy *mekugiProxy) {
	t.Helper()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.Close)
	proxy.replayStore = store
}

func newMekugiTestTransform(t *testing.T) (*mekugiResponseTransform, *mekugiProxy, *codexresponses.Request, string) {
	t.Helper()
	return newMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
}

func newMekugiTestTransformWithProxy(t *testing.T, proxy *mekugiProxy) (*mekugiResponseTransform, *mekugiProxy, *codexresponses.Request, string) {
	t.Helper()
	workspace := t.TempDir()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": "task"}}, "tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", codexresponses.TurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, proxy, &request, workspace
}

func newTopLevelMekugiTestTransformWithProxy(t *testing.T, proxy *mekugiProxy) (*mekugiResponseTransform, *codexresponses.Request) {
	t.Helper()
	workspace := t.TempDir()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{map[string]any{"role": "user", "content": "task"}},
		"tools": testExecResponsesTools(), "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "native-session", "native-thread", codexresponses.TurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, &request
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
	registry *frontends.Registry,
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
	environment = prependToolFrontendPath(environment, registry.FrontendDirectory)
	arguments := append(append([]string{}, interpreterArguments...), "-c", script)
	command := exec.CommandContext(t.Context(), execTrackShellExecutable(t, interpreter), arguments...)
	command.Dir, command.Env, command.Stdin = directory, environment, stdin
	var out, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostic
	runErr := command.Run()
	if runErr != nil {
		exit, ok := errors.AsType[*exec.ExitError](runErr)
		if !ok {
			t.Fatal(runErr)
		}
		exitCode = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			exitCode = 128 + int(status.Signal())
		}
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
	metadata := codexresponses.TurnMetadata{ThreadID: thread, ParentThreadID: parent, ForkedFromThreadID: fork}
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
