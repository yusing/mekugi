//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const toolFrontendCodexWorkerEnvironment = "MEKUGI_TOOL_FRONTEND_CODEX_WORKER"

// The registry pins this test executable. In the worker child, enter the same
// authenticated dispatch path as the real mekugi process before testing parses
// the configured tool's argv.
func init() {
	if os.Getenv(toolFrontendCodexWorkerEnvironment) == "1" && filepath.Base(os.Args[0]) == "stdin_tool" {
		if handled, code := RunToolPluginWorker(
			context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		); handled {
			os.Exit(code)
		}
		os.Exit(99)
	}
}

type toolFrontendCodexProvider struct {
	mu         sync.Mutex
	workspace  string
	turns      int
	callSent   bool
	resultSeen bool
}

func (p *toolFrontendCodexProvider) forwardExecution(
	_ context.Context,
	_ context.Context,
	body []byte,
	_ http.Header,
	_ string,
) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns++
	var item map[string]any
	switch {
	case !p.callSent:
		if !bytes.Contains(body, []byte("exec_command")) {
			return nil, fmt.Errorf("Codex request does not advertise stock exec_command")
		}
		p.callSent = true
		program := `const result = await tools.exec_command({"cmd":` +
			string(mustMarshalJSON("printf 'stdin is not worker JSON\\n' | stdin_tool one 'two words'")) +
			`,"workdir":` + string(mustMarshalJSON(p.workspace)) + `,"login":false});
text(JSON.stringify({output: result.output, exit_code: result.exit_code}));`
		item = map[string]any{
			"type": "custom_tool_call", "id": "frontend-item", "call_id": "frontend-call",
			"name": "exec", "status": "completed", "input": program,
		}
	default:
		var request struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		want := p.workspace + "|inherited|one|two words|stdin is not worker JSON"
		for _, input := range request.Input {
			if jsonString(input, "type") != "custom_tool_call_output" || jsonString(input, "call_id") != "frontend-call" {
				continue
			}
			output := string(input["output"])
			p.resultSeen = strings.Contains(output, want) && strings.Contains(output, `\"exit_code\":0`)
		}
		if !p.resultSeen {
			return nil, fmt.Errorf("stock exec_command result lacks authenticated frontend output: %.3000s", body)
		}
		item = map[string]any{
			"type": "message", "id": "frontend-finish", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "text": "frontend host accepted", "annotations": []any{},
			}},
		}
	}

	response := map[string]any{
		"id": fmt.Sprintf("frontend-response-%d", p.turns), "status": "completed", "output": []any{item},
		"usage": map[string]any{
			"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 15,
		},
	}
	var wire strings.Builder
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": response},
	} {
		if event["type"] == "response.output_item.done" && item["type"] == "function_call" {
			wire.WriteString("data: ")
			wire.Write(mustMarshalJSON(map[string]any{
				"type": "response.function_call_arguments.done", "item_id": item["id"], "arguments": item["arguments"],
			}))
			wire.WriteString("\n\n")
		}
		wire.WriteString("data: ")
		wire.Write(mustMarshalJSON(event))
		wire.WriteString("\n\n")
	}
	result := serverHTTPResponse(wire.String())
	result.Header.Set("Content-Type", "text/event-stream")
	return result, nil
}

func TestConfiguredToolFrontendNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for the frontend acceptance gate")
	}
	registry, _ := newToolPluginTestRegistryWithDeclaration(t, stdinToolPluginDeclaration)
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	frontend, ok := registry.frontends["stdin_tool"]
	if !ok {
		t.Fatal("stdin fixture frontend is unavailable")
	}
	workspace := t.TempDir()
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	provider := &toolFrontendCodexProvider{workspace: workspace}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
	defer server.Close()

	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Dir(frontend)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MEKUGI_PLUGIN_TEST", "inherited")
	t.Setenv(toolFrontendCodexWorkerEnvironment, "1")
	config := `model_providers.frontend_fixture={name="frontend_fixture",base_url=` +
		strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="frontend_fixture"`,
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", workspace, "Exercise the configured executable frontend fixture.",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("Codex frontend fixture: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || !provider.resultSeen || !strings.Contains(stdout.String(), "frontend host accepted") {
		t.Fatalf("frontend acceptance: turns=%d result=%t\nstdout: %s\nstderr: %s",
			provider.turns, provider.resultSeen, stdout.String(), stderr.String())
	}
}
