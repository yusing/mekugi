//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/appserver"
)

const toolFrontendCodexWorkerEnvironment = "MEKUGI_TOOL_FRONTEND_CODEX_WORKER"

func setCodexFrontendLoginEnvironment(t *testing.T, directory string) {
	t.Helper()
	quote := "'" + strings.ReplaceAll(directory, "'", "'\\''") + "'"
	startup := filepath.Join(t.TempDir(), "bash-env")
	if err := os.WriteFile(startup, []byte("PATH="+quote+":\"$PATH\"; export PATH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BASH_ENV", startup)
}

const stdinToolPluginDeclaration = `import { readFileSync } from "node:fs";
export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "frontend.stdin",
  tools: [{
    specification: {type: "custom", name: "stdin_tool", description: "stdin fixture"},
    parse(input) { return input; },
    argv(input) { return [input]; },
    execute(argv, context) {
      const input = context.stdinFD === null ? "" : readFileSync(context.stdinFD, "utf8").trimEnd();
      return {stdout: [process.cwd(), process.env.MEKUGI_PLUGIN_TEST, ...argv, input].join("|"), stderr: "", exitCode: 0};
    }
  }]
};`

// The registry pins this test executable. In the worker child, enter the same
// authenticated dispatch path as the real mekugi process before testing parses
// the configured tool's argv.
func init() {
	if len(os.Args) == 3 && os.Args[1] == "journal-mcp" {
		if err := RunJournalMCPBridge(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv(toolFrontendCodexWorkerEnvironment) == "1" && filepath.Base(os.Args[0]) == "stdin_tool" {
		if handled, code := RunOwnedToolPluginWorker(
			context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		); handled {
			os.Exit(code)
		}
		os.Exit(99)
	}
}

type toolFrontendCodexProvider struct {
	mu             sync.Mutex
	workspace      string
	program        string
	expected       []string
	finalText      string
	turns          int
	callSent       bool
	resultSeen     bool
	output         string                          // The tool call's output, as Codex returned it.
	observeRequest func([]byte, http.Header) error // Optional consuming-boundary assertion.
}

func (p *toolFrontendCodexProvider) forwardExecution(
	_ context.Context,
	_ context.Context,
	body []byte,
	headers http.Header,
	_ string,
) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns++
	if p.observeRequest != nil {
		if err := p.observeRequest(body, headers); err != nil {
			return nil, err
		}
	}
	var item map[string]any
	switch {
	case !p.callSent:
		if !bytes.Contains(body, []byte("exec_command")) {
			return nil, fmt.Errorf("Codex request does not advertise stock exec_command")
		}
		p.callSent = true
		program := p.program
		if program == "" {
			program = `const result = await tools.exec_command({"cmd":` +
				string(mustMarshalJSON("printf 'stdin is not worker JSON\\n' | stdin_tool one 'two words'")) +
				`,"workdir":` + string(mustMarshalJSON(p.workspace)) + `});
text(JSON.stringify({output: result.output, exit_code: result.exit_code}));`
		}
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
			p.output = output
			if len(p.expected) == 0 {
				p.resultSeen = strings.Contains(output, want) && strings.Contains(output, `\"exit_code\":0`)
				continue
			}
			p.resultSeen = true
			for _, expected := range p.expected {
				p.resultSeen = p.resultSeen && strings.Contains(output, expected)
			}
		}
		if !p.resultSeen {
			return nil, fmt.Errorf("stock tool result lacks expected output: %.1500s", p.output)
		}
		finalText := p.finalText
		if finalText == "" {
			finalText = "frontend host accepted"
		}
		item = map[string]any{
			"type": "message", "id": "frontend-finish", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "text": finalText, "annotations": []any{},
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
	dataDirectory := t.TempDir()
	pluginDirectory := filepath.Join(dataDirectory, "plugins")
	if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDirectory, "stdin.mjs"), []byte(stdinToolPluginDeclaration), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := buildToolRegistryForTest(t, t.Context(), dataDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
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
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()

	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setCodexFrontendLoginEnvironment(t, filepath.Dir(frontend))
	t.Setenv("MEKUGI_PLUGIN_TEST", "inherited")
	t.Setenv(toolFrontendCodexWorkerEnvironment, "1")
	config := `model_providers.frontend_fixture={name="frontend_fixture",base_url=` +
		strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="frontend_fixture"`, "-c", "features.plugins=false",
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

func TestMRunNativeCodexYieldAndWriteStdinE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for the mrun continuation acceptance gate")
	}
	registry, err := buildToolRegistryForTest(t, t.Context(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	frontend, ok := registry.frontends["mrun"]
	if !ok {
		t.Fatal("mrun session frontend is unavailable")
	}
	workspace := t.TempDir()
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	commandText := `mrun --max-tokens 100 -- sh -c 'printf ready > ready; IFS= read -r value; printf "continued:%s\n" "$value"'`
	program := `const started = await tools.exec_command({"cmd":` + string(mustMarshalJSON(commandText)) +
		`,"workdir":` + string(mustMarshalJSON(workspace)) + `,"tty":true,"yield_time_ms":250});
if (typeof started.session_id !== "number") throw new Error("mrun did not yield a host session");
const completed = await tools.write_stdin({"session_id":started.session_id,"chars":"codex-input\n","yield_time_ms":30000,"max_output_tokens":1000});
text(JSON.stringify({started, completed}));`
	provider := &toolFrontendCodexProvider{
		workspace: workspace,
		program:   program,
		expected:  []string{"continued:codex-input", `\"exit_code\":0`, "session_id"},
		finalText: "mrun continuation accepted",
	}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()

	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setCodexFrontendLoginEnvironment(t, filepath.Dir(frontend))
	t.Setenv(routerTestWorkerEnvironment, "1")
	config := `model_providers.frontend_fixture={name="frontend_fixture",base_url=` +
		strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="frontend_fixture"`, "-c", "features.plugins=false",
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", workspace, "Exercise mrun yielding and stock write_stdin continuation.",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("Codex mrun continuation fixture: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || !provider.resultSeen ||
		!strings.Contains(stdout.String(), "mrun continuation accepted") {
		t.Fatalf("mrun continuation acceptance: turns=%d result=%t\nstdout: %s\nstderr: %s",
			provider.turns, provider.resultSeen, stdout.String(), stderr.String())
	}
}

func TestJournalMCPNativeCodexReadE2E(t *testing.T) {
	testJournalMCPNativeCodex(t, "exec", false)
}

func TestJournalMCPNativeCodexMutateAutoReviewE2E(t *testing.T) {
	for _, mode := range []string{"app-server", "interactive"} {
		t.Run(mode, func(t *testing.T) { testJournalMCPNativeCodex(t, mode, true) })
	}
}

func testJournalMCPNativeCodex(t *testing.T, mode string, mutate bool) {
	t.Helper()
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for journal MCP acceptance")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workspace := t.TempDir()
	proxy := &mekugiProxy{journals: newJournalStore()}
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = replay
	socket, stop, err := startJournalMCP(t.Context(), newJournalMCPServer(proxy))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	provider := &toolFrontendCodexProvider{
		workspace: workspace,
		program: `const tool = ALL_TOOLS.find(t => t.name === "mcp__mekugi__journal_read");
if (!tool) throw new Error("journal MCP tool missing");
const result = await tools[tool.name]({view: "own", depth: 0});
if (result.isError) throw new Error(JSON.stringify(result));
text(result.structuredContent.nodes[0].title);`,
		expected:  []string{"authenticated journal MCP read"},
		finalText: "journal MCP host accepted",
	}
	if mutate {
		provider.program = `for (const title of ["first finished batch", "second finished batch"]) {
const result = await tools.mcp__mekugi__journal_mutate({mutations:[{op:"log",text:title},{op:"finish"}]});
if (result.isError) throw new Error(JSON.stringify(result));
text(title);
}`
		provider.expected = []string{"first finished batch", "second finished batch"}
	}
	provider.observeRequest = func(_ []byte, headers http.Header) error {
		metadata, _ := decodeCodexTurnMetadata(headers)
		thread := codexThreadID(headers)
		if metadata.ThreadID != thread || thread == "" {
			return fmt.Errorf("host journal identity missing")
		}
		ctx, release, err := replay.beginSession(t.Context(), thread, "seed")
		if err != nil {
			return err
		}
		defer release()
		if err := proxy.journals.initialize(ctx, replay, workspace, thread, "/root", ""); err != nil {
			return err
		}
		if err := proxy.journals.bindIdentity(ctx, replay, workspace, thread, "", "/root", true); err != nil {
			return err
		}
		_, err = proxy.journals.apply(ctx, replay, workspace, thread, "seed", []journalMutation{{Op: "add", Title: new("authenticated journal MCP read")}})
		return err
	}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	args := []string{
		"-c", `model_providers.frontend_fixture={name="frontend_fixture",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`,
		"-c", `model_provider="frontend_fixture"`, "-c", "features.plugins=false",
		"-c", `mcp_servers.mekugi={command=` + strconv.Quote(os.Args[0]) + `,args=["journal-mcp",` + strconv.Quote(socket) + `]}`,
		"-c", `model="gpt-6-astra"`,
	}
	approvalPolicy := "never"
	if mutate {
		approvalPolicy = "on-request"
		args = append(args, "-c", `approvals_reviewer="auto_review"`)
	}
	if mode != "app-server" {
		args = append(args, "--sandbox", "danger-full-access", "--ask-for-approval", approvalPolicy)
	}
	switch mode {
	case "exec":
		args = append(args, "exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never", "-C", workspace, "Exercise the journal MCP fixture.")
	case "app-server":
		args = append(args, "app-server")
	case "interactive":
		args = append(args, "-c", "projects."+strconv.Quote(workspace)+`.trust_level="trusted"`, "--no-alt-screen", "-C", workspace, "Exercise the journal MCP fixture.")
	}
	command := exec.CommandContext(ctx, codex, args...)
	command.Dir = workspace
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	switch mode {
	case "exec":
		if err := command.Run(); err != nil {
			t.Fatalf("Codex journal MCP: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
	case "app-server":
		runJournalMCPAppServer(t, ctx, command)
	case "interactive":
		command.Stdout, command.Stderr = nil, nil
		terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 120, Rows: 40})
		if err != nil {
			t.Fatal(err)
		}
		defer terminal.Close()
		defer func() { cancel(); _ = command.Wait() }()
		screen := vt.NewEmulator(120, 40)
		defer screen.Close()
		go func() { _, _ = io.Copy(terminal, screen) }()
		frames := readPTYChunks(ctx, terminal, 65536, 32)
		for !strings.Contains(screen.String(), provider.finalText) {
			select {
			case frame, ok := <-frames:
				if !ok {
					t.Fatalf("Codex terminal closed:\n%s", screen.String())
				}
				screen.Write(frame)
			case <-ctx.Done():
				t.Fatalf("Codex journal MCP terminal timed out:\n%s", screen.String())
			}
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || !provider.resultSeen {
		t.Fatalf("MCP acceptance: turns=%d result=%t\nstdout: %s\nstderr: %s", provider.turns, provider.resultSeen, stdout.String(), stderr.String())
	}
}

func runJournalMCPAppServer(t *testing.T, ctx context.Context, command *exec.Cmd) {
	t.Helper()
	client, err := appserver.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close(); <-client.Done }()
	initialize, err := client.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	var start string
	for {
		select {
		case message, ok := <-client.Messages:
			if !ok {
				t.Fatal("app-server closed before journal mutation completed")
			}
			if message.Error != nil {
				t.Fatalf("app-server RPC: %s", message.Error.Message)
			}
			if message.Method != "" && len(message.ID) != 0 {
				t.Fatalf("journal mutation requested client approval: %s", message.Method)
			}
			switch {
			case string(message.ID) == initialize:
				_, err = client.Send("initialized", map[string]any{}, false)
				if err == nil {
					start, err = client.Send("thread/start", map[string]any{"approvalPolicy": "on-request", "sandbox": "danger-full-access", "cwd": command.Dir}, true)
				}
			case start != "" && string(message.ID) == start:
				var result struct {
					Thread struct{ ID string } `json:"thread"`
				}
				if err = json.Unmarshal(message.Result, &result); err == nil {
					_, err = client.Send("turn/start", map[string]any{"threadId": result.Thread.ID, "input": appserver.Input("Exercise the journal MCP fixture.")}, true)
				}
			case message.Method == "turn/completed":
				var result struct{ Turn struct{ Status string } }
				if err := json.Unmarshal(message.Params, &result); err != nil || result.Turn.Status != "completed" {
					t.Fatalf("journal turn failed: %s, %v", message.Params, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
