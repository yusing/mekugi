//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

// Hold only the fork's final provider round so Main can finish independently
// after the real stock tool has executed and returned its result to Codex.
type oneoffCodexProvider struct {
	tools    toolFrontendCodexProvider
	requests chan btwCodexRequest
	finish   chan struct{}
}

func (p *oneoffCodexProvider) ForwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*inferencegateway.Response, error) {
	p.requests <- btwCodexRequest{bytes.Clone(body), key, headers.Clone()}
	if bytes.Contains(body, []byte("ONEOFF_TASK_ONLY")) {
		response, err := p.tools.ForwardExecution(ctx, responseCtx, body, headers, key)
		if err != nil {
			return nil, err
		}
		p.tools.mu.Lock()
		resultSeen := p.tools.resultSeen
		p.tools.mu.Unlock()
		if resultSeen {
			select {
			case <-p.finish:
			case <-ctx.Done():
				response.Body.Close()
				return nil, ctx.Err()
			}
		}
		return response, nil
	}
	response := routerFaultCodexSuccessResponse()
	wire, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	answer := "MAIN_SEED_ANSWER"
	if bytes.Contains(body, []byte("BTW_WHILE_ONEOFF")) {
		answer = "BTW_INDEPENDENT_ANSWER"
	}
	if bytes.Contains(body, []byte("Main continuation")) {
		answer = "MAIN_CONTINUATION_FINISHED"
	}
	response.Body = io.NopCloser(bytes.NewReader(bytes.ReplaceAll(wire, []byte("Recovered after a retry."), []byte(answer))))
	return testGatewayResponse(response, nil)
}

func TestAppServerOneoffNativeCodexE2E(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for one-off acceptance")
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "oneoff-written.txt")
	p := &oneoffCodexProvider{
		requests: make(chan btwCodexRequest, 12), finish: make(chan struct{}),
		tools: toolFrontendCodexProvider{
			program:  `const result = await tools.exec_command({cmd:"printf '%s' \"$MEKUGI_ONEOFF_TEST\" > oneoff-written.txt; cat oneoff-written.txt",login:false}); text(JSON.stringify({output:result.output,exit_code:result.exit_code}));`,
			expected: []string{"ONEOFF_ENV_INHERITED", `\"exit_code\":0`}, finalText: "ONEOFF_FINISHED",
		},
	}
	proxy := newManagedMekugiProxy(t)
	server := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, p, nil, proxy))
	defer server.Close()
	defer func() {
		select {
		case <-p.finish:
		default:
			close(p.finish)
		}
	}()
	environment := append(routerFaultCodexEnvironment(t), "MEKUGI_ONEOFF_TEST=ONEOFF_ENV_INHERITED")
	command := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.preview={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`,
			"-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false",
			"-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`)
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	terminal := startAppResumeTerminalWithIntegration(t, command, "", testAppServerIntegration(proxy))
	next := func() btwCodexRequest {
		t.Helper()
		select {
		case request := <-p.requests:
			return request
		case <-terminal.ctx.Done():
			t.Fatalf("missing provider request: %v\n%s", terminal.ctx.Err(), terminal.screen.String())
		}
		return btwCodexRequest{}
	}
	terminal.await("Ready")
	terminal.send("Main seed\r")
	main := next()
	terminal.await("completed")
	terminal.send("/oneoff ONEOFF_TASK_ONLY\r")
	fork := next()
	mainMetadata, _ := codexresponses.DecodeTurnMetadata(main.headers)
	forkMetadata, _ := codexresponses.DecodeTurnMetadata(fork.headers)
	if mainMetadata.ThreadID == "" || forkMetadata.ThreadID == "" || mainMetadata.ThreadID == forkMetadata.ThreadID {
		t.Fatalf("fork identities: Main=%q oneoff=%q", mainMetadata.ThreadID, forkMetadata.ThreadID)
	}
	if !bytes.Contains(fork.body, []byte("Main seed")) || !bytes.Contains(fork.body, []byte("Perform only requests submitted in this one-off conversation")) {
		t.Fatal("one-off omitted inherited history or task-scoping input")
	}
	var mainFields, forkFields map[string]any
	if err := json.Unmarshal(main.body, &mainFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fork.body, &forkFields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mainFields["tools"], forkFields["tools"]) {
		t.Fatal("one-off did not inherit Main's tools")
	}
	terminal.awaitMatch("one-off automatically selected", func(frame string) bool {
		return strings.Contains(frame, "┌ 1 One-off")
	})
	result := next()
	resultMetadata, _ := codexresponses.DecodeTurnMetadata(result.headers)
	if resultMetadata.ThreadID != forkMetadata.ThreadID || !bytes.Contains(result.body, []byte("custom_tool_call_output")) || !bytes.Contains(result.body, []byte("ONEOFF_ENV_INHERITED")) {
		t.Fatalf("stock tool result did not return within the fork: %s", result.body)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "ONEOFF_ENV_INHERITED" {
		t.Fatalf("one-off write/environment inheritance: contents=%q err=%v", contents, err)
	}
	terminal.send("/btw BTW_WHILE_ONEOFF\r")
	question := next()
	questionMetadata, _ := codexresponses.DecodeTurnMetadata(question.headers)
	if questionMetadata.ThreadID == "" || questionMetadata.ThreadID == forkMetadata.ThreadID || questionMetadata.ThreadID == mainMetadata.ThreadID || bytes.Contains(question.body, []byte("ONEOFF_TASK_ONLY")) {
		t.Fatal("BTW reused One-off's thread or history")
	}
	terminal.await("BTW_INDEPENDENT_ANSWER")
	terminal.await("/btw · completed")
	terminal.send("\x1b")
	terminal.awaitMatch("BTW closed while One-off remains", func(frame string) bool {
		return !strings.Contains(frame, "/btw ·") && strings.Contains(frame, "┌ 1 One-off")
	})
	terminal.send("\x021Main continuation\r")
	after := next()
	afterMetadata, _ := codexresponses.DecodeTurnMetadata(after.headers)
	if afterMetadata.ThreadID != mainMetadata.ThreadID || after.key != main.key {
		t.Fatal("Main continuation changed thread/cache identity")
	}
	for _, isolated := range []string{"ONEOFF_TASK_ONLY", "ONEOFF_ENV_INHERITED", "oneoff-written.txt", "frontend-call"} {
		if bytes.Contains(after.body, []byte(isolated)) {
			t.Fatalf("one-off injected %q into Main's continuation", isolated)
		}
	}
	terminal.await("MAIN_CONTINUATION_FINISHED")
	terminal.await("completed")
	close(p.finish)
	terminal.send("\x023nq")
	terminal.await("ONEOFF_FINISHED")
	terminal.awaitMatch("completed one-off retained", func(frame string) bool {
		return strings.Contains(frame, "┌ 1 One-off") && strings.Contains(frame, "╭─ completed") &&
			strings.Contains(frame, "Ran  printf") && strings.Contains(frame, "Read oneoff-written.txt") &&
			strings.Contains(frame, "┆ ONEOFF_ENV_INHERITED")
	})
	terminal.send("\x021")
	terminal.await("MAIN_CONTINUATION_FINISHED")
	terminal.quit()
}
