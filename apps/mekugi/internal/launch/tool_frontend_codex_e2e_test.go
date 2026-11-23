//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/libraries/mcptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func buildToolRegistryForTest(
	t *testing.T,
	ctx context.Context,
) (*frontends.Registry, error) {
	t.Helper()
	directory := t.TempDir()
	if _, err := openMekugiReplayStore(filepath.Join(directory, "replay")); err != nil {
		return nil, err
	}
	return frontends.New(ctx, frontends.Config{Commands: testCommands(), ReplayDirectory: filepath.Join(directory, "replay")})
}

func newShellWorkerTestInvocation(directory string, environment ...string) shellWorkerTestInvocation {
	base := append(os.Environ(), "CODEX_THREAD_ID=")
	return shellWorkerTestInvocation{directory: directory, environment: append(base, environment...)}
}

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

func init() {
	if len(os.Args) == 3 && os.Args[1] == "mcp-bridge" {
		if err := mcptransport.RelayStdio(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

type toolFrontendCodexProvider struct {
	mu             sync.Mutex
	program        string
	expected       []string
	finalText      string
	turns          int
	callSent       bool
	resultSeen     bool
	output         string                          // The tool call's output, as Codex returned it.
	observeRequest func([]byte, http.Header) error // Optional consuming-boundary assertion.
}

func (p *toolFrontendCodexProvider) ForwardExecution(
	_ context.Context,
	_ context.Context,
	body []byte,
	headers http.Header,
	_ string,
) (*inferencegateway.Response, error) {
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
		item = map[string]any{
			"type": "custom_tool_call", "id": "frontend-item", "call_id": "frontend-call",
			"name": "exec", "status": "completed", "input": p.program,
		}
	default:
		var request struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		for _, input := range request.Input {
			if responses.StringField(input, "type") != "custom_tool_call_output" || responses.StringField(input, "call_id") != "frontend-call" {
				continue
			}
			output := string(input["output"])
			p.output = output
			p.resultSeen = true
			for _, expected := range p.expected {
				p.resultSeen = p.resultSeen && strings.Contains(output, expected)
			}
		}
		if !p.resultSeen {
			return nil, fmt.Errorf("stock tool result lacks expected output: %.1500s", p.output)
		}
		item = map[string]any{
			"type": "message", "id": "frontend-finish", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "text": p.finalText, "annotations": []any{},
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
			wire.Write(responses.MustMarshalJSON(map[string]any{
				"type": "response.function_call_arguments.done", "item_id": item["id"], "arguments": item["arguments"],
			}))
			wire.WriteString("\n\n")
		}
		wire.WriteString("data: ")
		wire.Write(responses.MustMarshalJSON(event))
		wire.WriteString("\n\n")
	}
	result := serverHTTPResponse(wire.String())
	result.Header.Set("Content-Type", "text/event-stream")
	return testGatewayResponse(result, nil)
}

// Check bundled mrun's process inputs and the stock PTY input path.
func TestAppServerNativeMRunAndPTY(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bash := execTrackShellExecutable(t, "bash")
	mrun := `test "$(command -v mrun)" = "$CODEX_ARG0_PATH_DIR/mrun" || exit 1
printf 'piped input\n' | mrun --max-tokens 1024 -- sh -c 'printf "%s|%s|%s|%s|" "$PWD" "$MEKUGI_FRONTEND_TEST" "$1" "$2"; cat' sh one 'two words'`
	pty := `IFS= read -r value; printf 'continued:%s\n' "$value"`
	shell := string(responses.MustMarshalJSON(bash))
	provider := &toolFrontendCodexProvider{
		program: `const bounded = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(mrun)) +
			`,shell:` + shell + `,login:false,workdir:` + string(responses.MustMarshalJSON(workspace)) + `});
if (bounded.exit_code !== 0) throw new Error(JSON.stringify(bounded));
text(bounded.output);
const started = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(pty)) +
			`,shell:` + shell + `,login:false,tty:true,yield_time_ms:250});
if (typeof started.session_id !== "number") throw new Error("PTY command did not yield a process session");
const completed = await tools.write_stdin({session_id:started.session_id,chars:"codex-input\n",yield_time_ms:5000});
if (completed.exit_code !== 0) throw new Error(JSON.stringify(completed));
text(completed.output);`,
		expected:  []string{workspace + "|inherited|one|two words|piped input", "continued:codex-input"},
		finalText: "Recovered after a retry.",
	}
	runAppServerPreviewWith(t, provider, nil, appServerPreview{
		managedRuntime: true,
		environment:    []string{"SHELL=" + bash, "MEKUGI_FRONTEND_TEST=inherited"},
	})
}
