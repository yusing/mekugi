//go:build journal_e2e

package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	finishes           int
	finishOutputs      []string
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
			Role    string         `json:"role"`
			CallID  string         `json:"call_id"`
			Output  jsontext.Value `json:"output"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Text struct {
			Format struct {
				Type   string         `json:"type"`
				Strict bool           `json:"strict"`
				Schema jsontext.Value `json:"schema"`
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
		if !strings.Contains(assignment, "Otherwise keep reason empty.") || !strings.Contains(assignment, "Return the JSON verdict as your final answer.") {
			return nil, fmt.Errorf("review assignment omitted verdict completion rules")
		}
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
		var gotSchema, wantSchema any
		if err := json.Unmarshal(request.Text.Format.Schema, &gotSchema); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(orchestrate.ReviewResultSchema), &wantSchema); err != nil {
			return nil, err
		}
		if request.Text.Format.Type != "json_schema" || !request.Text.Format.Strict || !reflect.DeepEqual(gotSchema, wantSchema) {
			return nil, fmt.Errorf("host omitted review output schema: %s", request.Text.Format.Type)
		}
		if call == 1 {
			p.schemas++
			args, _ := json.Marshal(map[string]any{"cmd": "printf forbidden > " + strconv.Quote(filepath.Join(p.checkout, "readonly-probe")), "workdir": p.checkout, "shell": "bash"})
			return mchangesNestedCodexResponse(call, map[string]any{"type": "custom_tool_call", "id": "readonly-" + metadata.ThreadID, "call_id": "readonly-" + metadata.ThreadID, "name": "exec", "input": "text(await tools.exec_command(" + string(args) + "));", "status": "completed"}), nil
		}
		if call == 2 {
			// A successful host finish must still reach the provider for the
			// constrained verdict, rather than synthesize a Journal final.
			return mchangesNestedCodexResponse(call, map[string]any{"type": "custom_tool_call", "id": "finish-" + metadata.ThreadID, "call_id": "finish-" + metadata.ThreadID, "name": "exec", "status": "completed", "input": `const check = await tools.exec_command({cmd:"printf reviewed"}); if(check.exit_code !== 0) throw new Error("review check failed"); const r = await tools.mcp__mekugi__journal_mutate({mutations:[{op:"log",text:"Source review complete"},{op:"finish"}]}); if(r.isError) throw new Error(JSON.stringify(r)); text(r);`}), nil
		}
		finished := false
		for _, item := range request.Input {
			if texts := executionOutputTexts([]byte(item.Output)); item.CallID == "finish-"+metadata.ThreadID && len(texts) > 0 {
				p.finishOutputs = append(p.finishOutputs, texts...)
				status, _, _ := codeModeExecutionHeader(texts[0])
				finished = status == "Script completed"
			}
		}
		if finished {
			p.finishes++
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
	attachTestReplayStore(t, proxy)
	proxy.nativeTrace = &nativeToolTrace{directory: trace}
	socket, stopMCP, err := startJournalMCP(t.Context(), newJournalMCPServer(proxy))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stopMCP(); err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.workflow_test={name="workflow_test",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="workflow_test"`, "-c", `model="gpt-6-astra"`, "-c", `model_reasoning_effort="high"`, "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", `approval_policy="never"`, "-c", `sandbox_mode="workspace-write"`)
	cmd.Args = append(cmd.Args, "-c", `mcp_servers.mekugi={command=`+strconv.Quote(os.Args[0])+`,args=["journal-mcp",`+strconv.Quote(socket)+`]}`)
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
				if message.Method == "turn/completed" && strings.Contains(string(message.Params), `"failed"`) {
					t.Logf("failed host turn: %s", message.Params)
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
	if provider.finishes != 2 {
		t.Errorf("missing successful journal finishes: %q", provider.finishOutputs)
	}
	schemas := provider.schemas
	provider.mu.Unlock()
	if schemas != 2 || len(child.batch.Workflow.Reviews) != 2 || len(child.batch.Workflow.Checks) != 1 || child.batch.Workflow.Checks[0].Outcome != orchestrate.CheckPassed {
		t.Fatal("missing schema review or validation evidence", schemas)
	}
	if !configured {
		testOrchestrateArchiveNativeCodex(t, u, pump)
	}
}
