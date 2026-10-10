//go:build journal_e2e

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestrateExitProvider struct {
	workspace         string
	runtime           *orchestrateRuntime
	started, canceled chan string
	failures          chan error
}

func (p *orchestrateExitProvider) forwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	name := "batch"
	if strings.Contains(string(body), "Keep Main running.") {
		name = "main"
		meta, _ := decodeCodexTurnMetadata(headers)
		thread := meta.ThreadID
		if thread == "" {
			thread = headers.Get(threadIDHeader)
		}
		if _, err := p.runtime.store.Prepare(ctx, p.workspace, thread, "batch"); err != nil {
			p.failures <- err
			return nil, err
		}
		command := &orchestrateCommand{ctx: ctx, workspace: p.workspace, main: thread,
			input: orchestrateSpawnInput{TaskName: "batch", Message: "Keep batch running."}, reply: make(chan orchestrateResult, 1)}
		if _, _, err := p.runtime.call(command); err != nil {
			p.failures <- err
			return nil, err
		}
	}
	p.started <- name
	<-responseCtx.Done()
	p.canceled <- name
	return nil, responseCtx.Err()
}

func TestAppServerOrchestrateExitNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	workspace := gitTestWorkspace(t)
	writeTestFile(t, workspace+"/file", "base")
	gitTestCommit(t, workspace)
	proxy := newManagedMekugiProxy(t)
	proxy.orchestration = &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}, commands: make(chan *orchestrateCommand)}
	provider := &orchestrateExitProvider{workspace: workspace, runtime: proxy.orchestration, started: make(chan string, 2), canceled: make(chan string, 2), failures: make(chan error, 2)}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	defer server.Close()
	environment := routerFaultCodexEnvironment(t)
	terminal := startAppResumeTerminalWithProxy(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.exit_fixture={name="exit_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="exit_fixture"`, "-c", `model="gpt-6-astra"`, "-c", `approval_policy="never"`, "-c", `default_permissions=":workspace"`, "-c", "include_collaboration_mode_instructions=false")
		cmd.Dir, cmd.Env = workspace, environment
		return cmd
	}, "", proxy)
	terminal.await("Ready")
	terminal.send("Keep Main running.\r")
	terminal.awaitMatch("two running threads", func(frame string) bool {
		select {
		case err := <-provider.failures:
			t.Fatal("fixture launch failed", err)
		default:
		}
		return len(provider.started) == 2 && strings.Contains(frame, "batch")
	})
	terminal.send("/quit\r")
	terminal.await("Quit all threads?")
	terminal.send("\x1b")
	terminal.awaitMatch("declined exit", func(frame string) bool { return !strings.Contains(frame, "Quit all threads?") })
	select {
	case name := <-provider.canceled:
		t.Fatal("decline canceled", name)
	default:
	}
	terminal.send("\r") // The declined /quit draft is still present.
	terminal.await("Quit all threads?")
	terminal.send("\r")
	select {
	case err := <-terminal.done:
		if err != nil {
			t.Fatal("confirmed exit failed", err)
		}
	case <-terminal.ctx.Done():
		t.Fatal("confirmed exit did not finish", terminal.ctx.Err())
	}
	for range 2 {
		select {
		case <-provider.canceled:
		case <-terminal.ctx.Done():
			t.Fatal("host left a thread running")
		}
	}
	terminal.checkRestored()
}
