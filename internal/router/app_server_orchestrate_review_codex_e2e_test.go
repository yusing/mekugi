//go:build journal_e2e

package router

import (
	"context"
	json "encoding/json/v2"
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

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type workflowCodexProvider struct {
	mu                 sync.Mutex
	calls              map[string]int
	checkout, revision string
	schemas            int
	configured         bool
}

func (p *workflowCodexProvider) forwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*http.Response, error) {
	metadata, _ := decodeCodexTurnMetadata(headers)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[metadata.ThreadID+"/"+metadata.TurnID]++
	call := p.calls[metadata.ThreadID+"/"+metadata.TurnID]
	var request struct {
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
		Reasoning   struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Text struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	assignment := ""
	for _, item := range request.Input {
		if item.Role == "user" {
			for _, content := range item.Content {
				assignment = content.Text
			}
		}
	}
	final := "Implementation complete."
	if strings.HasPrefix(assignment, "Review candidate") {
		purpose := orchestrate.DispatchCorrectness
		if strings.Contains(assignment, "for simplification.") {
			purpose = orchestrate.DispatchSimplification
		}
		instructions := workflowReviewerInstructions(purpose)
		if p.configured && purpose == orchestrate.DispatchCorrectness {
			instructions = "Configured correctness rubric"
			if request.Model != "gpt-6-sol" || request.Reasoning.Effort != "low" || request.ServiceTier != "flex" {
				return nil, fmt.Errorf("configured role settings missing: model=%s effort=%s tier=%s", request.Model, request.Reasoning.Effort, request.ServiceTier)
			}
		} else if request.Model != "gpt-6-astra" || request.Reasoning.Effort != "high" || request.ServiceTier != "" {
			return nil, fmt.Errorf("unmapped reviewer lost inherited settings: model=%s effort=%s tier=%s", request.Model, request.Reasoning.Effort, request.ServiceTier)
		}
		found := false
		for _, item := range request.Input {
			if item.Role == "developer" {
				for _, content := range item.Content {
					found = found || strings.Contains(content.Text, instructions)
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("review developer instructions missing for %s", purpose)
		}
		if request.Text.Format.Type != "json_schema" {
			return nil, fmt.Errorf("host omitted review output schema: %s", request.Text.Format.Type)
		}
		if call == 1 {
			p.schemas++
			args, _ := json.Marshal(map[string]any{"cmd": "printf forbidden > " + strconv.Quote(filepath.Join(p.checkout, "readonly-probe")), "workdir": p.checkout, "shell": "bash"})
			return mchangesNestedCodexResponse(call, map[string]any{"type": "custom_tool_call", "id": "readonly", "call_id": "readonly", "name": "exec", "input": "text(await tools.exec_command(" + string(args) + "));", "status": "completed"}), nil
		}
		raw, _ := json.Marshal(orchestrate.ReviewResult{Revision: p.revision, Purpose: orchestrate.ReviewPurpose(purpose), Verdict: orchestrate.VerdictClear, Findings: []orchestrate.ReviewFinding{}})
		final = string(raw)
	}
	return mchangesNestedCodexResponse(call, map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": final}}}), nil
}

func TestOrchestrateWorkflowNativeCodex(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint("configured=", configured), func(t *testing.T) { testOrchestrateWorkflowNativeCodex(t, configured) })
	}
}
func testOrchestrateWorkflowNativeCodex(t *testing.T, configured bool) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	workspace := orchestrateVCSWorkspace(t, "git")
	trace := t.TempDir()
	t.Setenv("CODEX_ROLLOUT_TRACE_ROOT", trace)
	provider := &workflowCodexProvider{calls: map[string]int{}, configured: configured}
	proxy := newManagedMekugiProxy(t)
	proxy.nativeTrace = &nativeToolTrace{directory: trace}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.workflow_test={name="workflow_test",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="workflow_test"`, "-c", `model="gpt-6-astra"`, "-c", `model_reasoning_effort="high"`, "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", `approval_policy="never"`, "-c", `sandbox_mode="workspace-write"`)
	{
		role := filepath.Join(t.TempDir(), "role.toml")
		if err := os.WriteFile(role, []byte("developer_instructions = 'Configured correctness rubric'\nmodel = 'gpt-6-sol'\nmodel_reasoning_effort = 'low'\nservice_tier = 'flex'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		// Available roles are not selected unless mapped by Mekugi.
		for _, name := range []string{"custom-audit", "review-correctness", "review-simplify"} {
			cmd.Args = append(cmd.Args, "-c", "agents."+name+"={description=\"Configured correctness\",config_file="+strconv.Quote(role)+"}")
		}
	}
	cmd.Env, cmd.Dir = routerFaultCodexEnvironment(t), workspace
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := &appServerUI{ctx: ctx, client: client, proxy: proxy, view: newLiveActivityView(), agents: newLiveActivityView(), requests: map[string]string{}, notifications: &nativeNotifications{out: io.Discard}, resumeCwd: workspace}
	u.ensureShell()
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	defer u.closeOrchestrateStorage()
	defer u.closeOrchestratedViews()
	pump := func(done func() bool) {
		t.Helper()
		for !done() {
			select {
			case message, ok := <-client.Messages:
				if !ok {
					t.Fatal("host closed")
				}
				if err := u.message(message); err != nil {
					t.Fatal(err)
				}
			case complete := <-u.orchestrateCompletions:
				u.completeOrchestrateWork(complete)
			case <-ctx.Done():
				t.Fatalf("workflow timeout: %s; requests=%v", u.notice, u.orchestrateRequests)
			}
		}
	}
	if err := u.request("initialize", nil); err != nil {
		t.Fatal(err)
	}
	pump(func() bool { return u.thread != "" && !u.modelsLoading })
	main := u.thread
	ctx, release, err := proxy.replayStore.beginSession(ctx, main, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := proxy.journals.initialize(ctx, proxy.replayStore, workspace, main, "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(ctx, proxy.replayStore, workspace, main, "", "/root", true); err != nil {
		t.Fatal(err)
	}
	store := &orchestrate.Store{Directory: t.TempDir()}
	proxy.orchestration = &orchestrateRuntime{store: store}
	if configured {
		proxy.orchestration.reviewers = loadWorkflowReviewerTestConfig(t, "[orchestration.reviewers]\ncorrectness = 'custom-audit'\n")
	}
	batch, err := store.Prepare(ctx, workspace, main, "batch")
	if err != nil {
		t.Fatal(err)
	}
	provider.checkout, provider.revision = batch.Cwd, batch.Base
	command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, tool: "spawn_agent", input: orchestrateSpawnInput{TaskName: "batch", Message: "Complete the bounded assignment without changing files.", RequiredChecks: []string{"printf workflow-check"}}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(command)
	pump(func() bool { return len(command.reply) > 0 })
	result := <-command.reply
	if result.err != nil {
		t.Fatal(result.err)
	}
	pump(func() bool {
		child := u.orchestrateThreads[result.batch.Launch.ThreadID]
		return child != nil && child.batch.Workflow.Outcome != nil && child.batch.Workflow.Outcome.State != orchestrate.DispatchIntent
	})
	child := u.orchestrateThreads[result.batch.Launch.ThreadID]
	if child.batch.Workflow.Phase != orchestrate.PhaseReady {
		provider.mu.Lock()
		t.Logf("provider calls: %v", provider.calls)
		provider.mu.Unlock()
		t.Fatalf("workflow not ready: %+v; notice=%s", child.batch.Workflow, u.notice)
	}
	if child.batch.Workflow.Outcome.State != orchestrate.DispatchAcknowledged {
		t.Fatal("outcome unacknowledged")
	}
	if _, err := os.Stat(filepath.Join(batch.Cwd, "readonly-probe")); !os.IsNotExist(err) {
		t.Fatal("reviewer escaped read-only sandbox", err)
	}
	provider.mu.Lock()
	schemas := provider.schemas
	provider.mu.Unlock()
	if schemas != 2 || len(child.batch.Workflow.Reviews) != 2 || len(child.batch.Workflow.Checks) != 1 || child.batch.Workflow.Checks[0].Outcome != orchestrate.CheckPassed {
		t.Fatal("missing schema review or validation evidence", schemas)
	}
}
