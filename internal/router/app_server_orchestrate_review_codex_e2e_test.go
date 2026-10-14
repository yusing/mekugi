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
	runtimeChecks      int
	finishOutputs      []string
	configured         bool
	reuse              bool
	rounds             map[string]int
	compactions        int
	responses          int
}

func (p *workflowCodexProvider) forwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*http.Response, error) {
	metadata, _ := decodeCodexTurnMetadata(headers)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses++
	identity := metadata.ThreadID + "-" + metadata.TurnID
	p.calls[metadata.ThreadID+"/"+metadata.TurnID]++
	call := p.calls[metadata.ThreadID+"/"+metadata.TurnID]
	if metadata.RequestKind == "compaction" {
		p.compactions++
		return nil, fmt.Errorf("reviewer reset reached the provider")
	}
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
	assignment, recovered := "", ""
	for _, item := range request.Input {
		for _, content := range item.Content {
			if strings.Contains(content.Text, "Reviewer recovery") {
				recovered = content.Text
			}
		}
		if item.Role == "user" {
			for _, content := range item.Content {
				assignment = content.Text
			}
		}
	}
	final := "Implementation complete."
	if strings.Contains(assignment, "Review candidate") {
		evidence := recovered + "\n" + assignment
		if !strings.Contains(assignment, "Review candidate "+p.revision+" ") || !strings.Contains(evidence, "Runtime validation receipt (host command/exec): ") || !strings.Contains(evidence, `"revision":"`+p.revision+`"`) || !strings.Contains(evidence, `"exit_code":0`) || !strings.Contains(evidence, `"output":"workflow-check"`) || !strings.Contains(evidence, "Implementer report (claims, not host validation receipts):") {
			return nil, fmt.Errorf("review assignment omitted exact-candidate host receipt or report provenance")
		}
		if !strings.Contains(assignment, "JSON verdict as your final answer.") {
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
			if p.rounds == nil {
				p.rounds = map[string]int{}
			}
			p.rounds[metadata.ThreadID]++
		}
		if p.rounds[metadata.ThreadID] == 4 {
			for _, duplicate := range []string{"Prior review (", "Runtime validation receipt (", "Implementer report ("} {
				if strings.Contains(assignment, duplicate) {
					return nil, fmt.Errorf("post-reset continuation repeats recovered %q", duplicate)
				}
			}
			for _, want := range []string{"Reviewer recovery", "Purpose: " + string(purpose), p.revision, "Journal recovery", "Prior review (retained verdict and dispatch provenance)"} {
				if !strings.Contains(recovered, want) {
					return nil, fmt.Errorf("local reviewer reset lost %q", want)
				}
			}
		}
		if p.rounds[metadata.ThreadID] == 4 {
			if strings.Contains(recovered, "Dispute this fixture finding with evidence") != (purpose == orchestrate.DispatchCorrectness) {
				return nil, fmt.Errorf("reset mixed reviewer purpose findings for %s", purpose)
			}
		}
		if call == 1 {
			p.schemas++
			command := `set -eu; test -n "$TMPDIR"; printf owned > "$TMPDIR/owned-probe"; `
			// Workflow/release acceptance covers real Go builds. Reuse checks the
			// same scratch/source boundary without rebuilding per reset scenario.
			if !p.reuse {
				command += `env GOTOOLCHAIN=local GOFLAGS=-p=1 GOMAXPROCS=2 GOMEMLIMIT=700MiB go test -run '^TestReviewerScratch$' -count=1 .; `
			}
			command += `if printf forbidden > readonly-probe 2>/dev/null; then exit 9; fi; printf runtime-review-check-ok`
			args, _ := json.Marshal(map[string]any{"cmd": command, "workdir": p.checkout, "shell": "bash", "login": false, "yield_time_ms": 1000})
			input := "// @exec: {\"yield_time_ms\": 120000}\nlet r = await tools.exec_command(" + string(args) + "); text(r.output); while (r.session_id) { r = await tools.write_stdin({session_id:r.session_id,chars:\"\",yield_time_ms:1000}); text(r.output); } if(r.exit_code !== 0) throw new Error(\"review check failed: \"+JSON.stringify(r));"
			return mchangesNestedCodexResponse(p.responses, map[string]any{"type": "custom_tool_call", "id": "readonly-" + identity, "call_id": "readonly-" + identity, "name": "exec", "input": input, "status": "completed"}), nil
		}
		if call == 2 {
			checked := false
			for _, item := range request.Input {
				if item.CallID == "readonly-"+identity {
					checked = strings.Contains(strings.Join(executionOutputTexts([]byte(item.Output)), "\n"), "runtime-review-check-ok")
				}
			}
			if !checked {
				return nil, fmt.Errorf("reviewer runtime check or source isolation failed")
			}
			p.runtimeChecks++
			// A successful host finish must still reach the provider for the
			// constrained verdict, rather than synthesize a Journal final.
			return mchangesNestedCodexResponse(p.responses, map[string]any{"type": "custom_tool_call", "id": "finish-" + identity, "call_id": "finish-" + identity, "name": "exec", "status": "completed", "input": `const check = await tools.exec_command({cmd:"printf reviewed"}); if(check.exit_code !== 0) throw new Error("review check failed"); const r = await tools.mcp__mekugi__journal_mutate({mutations:[{op:"log",text:"Source review complete"},{op:"finish"}]}); if(r.isError) throw new Error(JSON.stringify(r)); text(r);`}), nil
		}
		finished := false
		for _, item := range request.Input {
			if texts := executionOutputTexts([]byte(item.Output)); item.CallID == "finish-"+identity && len(texts) > 0 {
				p.finishOutputs = append(p.finishOutputs, texts...)
				status, _, _ := codeModeExecutionHeader(texts[0])
				finished = status == "Script completed"
			}
		}
		if finished {
			p.finishes++
		}
		result := orchestrate.ReviewResult{Revision: p.revision, Purpose: orchestrate.ReviewPurpose(purpose), Verdict: orchestrate.VerdictClear, Findings: []orchestrate.ReviewFinding{}}
		if p.reuse && purpose == orchestrate.DispatchCorrectness && p.rounds[metadata.ThreadID] >= 2 && p.rounds[metadata.ThreadID] <= 3 {
			result.Verdict, result.Findings = orchestrate.VerdictFindings, []orchestrate.ReviewFinding{{ID: "F1", Detail: "Dispute this fixture finding with evidence"}}
		}
		raw, _ := json.Marshal(result)
		final = string(raw)
	}
	return mchangesNestedCodexResponse(p.responses, map[string]any{"type": "message", "id": "answer-" + identity, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": final}}}), nil
}

func TestOrchestrateWorkflowNativeCodex(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint("configured=", configured), func(t *testing.T) { testOrchestrateWorkflowNativeCodex(t, configured, "") })
	}
}
func TestOrchestrateReviewerReuseNativeCodex(t *testing.T) {
	for _, mode := range []string{"off", "slice", "unavailable"} {
		t.Run(mode, func(t *testing.T) { testOrchestrateWorkflowNativeCodex(t, true, mode) })
	}
}
func testOrchestrateWorkflowNativeCodex(t *testing.T, configured bool, resetMode string) {
	reuse := resetMode != ""
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	workspace := orchestrateVCSWorkspace(t, "git")
	writeTestFile(t, filepath.Join(workspace, "go.mod"), "module reviewerfixture\n\ngo 1.27\n")
	writeTestFile(t, filepath.Join(workspace, "scratch_test.go"), "package reviewerfixture\nimport (\"os\"; \"path/filepath\"; \"testing\")\nfunc TestReviewerScratch(t *testing.T) { if err := os.WriteFile(filepath.Join(t.TempDir(), \"result\"), []byte(\"ok\"), 0600); err != nil { t.Fatal(err) } }\n")
	gitTestCommit(t, workspace)
	trace := t.TempDir()
	t.Setenv("CODEX_ROLLOUT_TRACE_ROOT", trace)
	provider := &workflowCodexProvider{calls: map[string]int{}, configured: configured, reuse: reuse}
	proxy := newManagedMekugiProxy(t)
	proxy.journalCompaction = "off"
	if resetMode == "slice" {
		proxy.journalCompaction = "slice"
	}
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
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.workflow_test={name="workflow_test",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="workflow_test"`, "-c", `model="gpt-6-astra"`, "-c", `model_reasoning_effort="high"`, "-c", "features.token_budget=true", "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", `approval_policy="never"`, "-c", `sandbox_mode="workspace-write"`)
	cmd.Args = append(cmd.Args, "-c", `mcp_servers.mekugi={command=`+strconv.Quote(os.Args[0])+`,args=["journal-mcp",`+strconv.Quote(socket)+`]}`)
	releaseDirectory := os.Getenv(reviewerReleaseProbe)
	if releaseDirectory != "" {
		cmd.Args = append(cmd.Args, "-c", `mcp_servers.mekugi={command=`+strconv.Quote(os.Args[0])+`,args=["review-release-mcp",`+strconv.Quote(socket)+`,`+strconv.Quote(releaseDirectory)+`]}`)
	}
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
	releaseEvidence := map[string]reviewerReleaseEvidence{}
	reviewResumes := map[string]int{}
	pump := func(done func() bool) {
		t.Helper()
		for !done() {
			select {
			case message, ok := <-client.Messages:
				if !ok {
					t.Fatal("host closed")
				}
				if request := u.orchestrateRequests[string(message.ID)]; message.Method == "" && request.method == "thread/resume" && request.review != nil {
					reviewResumes[request.review.dispatch.ThreadID]++
				}
				observeReviewerRelease(t, cmd.Process.Pid, releaseDirectory, message, releaseEvidence)
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
	// Stop the isolated host before fixture cleanup, including failure paths.
	defer func() {
		if err := client.Shutdown(); err != nil {
			t.Error("review host shutdown", err)
			return
		}
		if err := u.closeOrchestrateStorage(); err != nil {
			t.Error(err)
		}
		if err := u.releaseWorkflowReviewStorage(); err != nil {
			t.Error(err)
		}
		if releaseDirectory != "" {
			verifyReviewerRelease(t, cmd.Process.Pid, releaseEvidence)
		}
		batches, err := store.Snapshot(workspace, main)
		if err != nil {
			t.Error(err)
			return
		}
		for _, b := range batches {
			if b.Workflow == nil {
				continue
			}
			for _, d := range b.Workflow.Dispatches {
				if d.Scratch == "" {
					continue
				}
				if _, err := os.Stat(d.Scratch); !os.IsNotExist(err) {
					t.Error("review scratch survived confirmed host shutdown", err)
				}
			}
		}
	}()
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
	if provider.runtimeChecks != 2 {
		t.Errorf("reviewer runtime checks = %d, want 2", provider.runtimeChecks)
	}
	if provider.finishes != 2 {
		t.Errorf("missing successful journal finishes: %q", provider.finishOutputs)
	}
	schemas := provider.schemas
	provider.mu.Unlock()
	if schemas != 2 || len(child.batch.Workflow.Reviews) != 2 || len(child.batch.Workflow.Checks) != 1 || child.batch.Workflow.Checks[0].Outcome != orchestrate.CheckPassed {
		t.Fatal("missing schema review or validation evidence", schemas)
	}
	u.orchestrationRoster()
	if len(u.agents.orchestration) != 4 || len(u.navigation.pending) != 0 || len(u.navigation.reads) != 0 {
		t.Fatal("host reviewer roots missing or stranded", u.agents.orchestration, u.navigation.pending, u.navigation.reads)
	}
	for _, d := range child.batch.Workflow.Dispatches {
		if d.Kind != orchestrate.DispatchCorrectness && d.Kind != orchestrate.DispatchSimplification {
			continue
		}
		v := u.navigation.views[d.ThreadID]
		if v == nil || v.workflowReview == nil {
			t.Fatal("review has no independent view", d)
		}
		a := v.session.agent("/root")
		v.observeCost(d.ThreadID, a)
		knownPrice := !configured || d.Kind != orchestrate.DispatchCorrectness // The configured flex tier has no reference price.
		if a.InputTokens != 30 || a.OutputTokens != 15 || a.Roundtrips != 3 || a.CostKnown != knownPrice || a.Responding {
			t.Fatalf("review usage or lifecycle missing: %+v", a)
		}
	}
	foundCheck := false
	for _, entry := range u.navigation.views[child.batch.Launch.ThreadID].view.entries {
		if entry.native != nil && entry.native.command == "printf workflow-check" {
			foundCheck = !entry.native.running && strings.Join(entry.outputTail, "\n") == "workflow-check"
		}
	}
	if !foundCheck {
		t.Fatal("host validation output did not reach the batch view")
	}
	if reuse {
		pump(func() bool { return len(u.orchestrateJobs) == 0 && len(u.orchestrateRequests) == 0 })
		if err := client.Shutdown(); err != nil {
			t.Fatal(err)
		}
		if err := u.releaseWorkflowReviewStorage(); err != nil {
			t.Fatal(err)
		}
		restarted := exec.CommandContext(ctx, codex, cmd.Args[1:]...)
		restarted.Env, restarted.Dir = cmd.Env, cmd.Dir
		client, err = appserver.Start(restarted)
		if err != nil {
			t.Fatal(err)
		}
		u.client = client
		for _, view := range u.navigation.views {
			view.client = client
		}
		id, err := client.Initialize()
		if err != nil {
			t.Fatal(err)
		}
		awaitHistoryRPC(t, client, id)
		if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
			t.Fatal(err)
		}
		for _, thread := range []string{main, child.batch.Launch.ThreadID} {
			id, err := client.Send("thread/resume", map[string]any{"threadId": thread}, true)
			if err != nil {
				t.Fatal(err)
			}
			if result := awaitHistoryRPC(t, client, id); result.Error != nil {
				t.Fatal(result.Error)
			}
		}
		if resetMode == "unavailable" {
			for _, d := range child.batch.Workflow.Dispatches {
				if d.Kind == orchestrate.DispatchCorrectness {
					if err := os.Mkdir(filepath.Join(proxy.replayStore.directory, journalCompactionName(child.batch.Cwd, d.ThreadID)), 0700); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
		}
		writeTestFile(t, filepath.Join(child.batch.Cwd, "candidate.txt"), "current candidate after initial review\n")
		gitTestCommit(t, child.batch.Cwd)
		revisionCommand := exec.Command("git", "rev-parse", "HEAD")
		revisionCommand.Dir = child.batch.Cwd
		revision, err := revisionCommand.Output()
		if err != nil {
			t.Fatal(err)
		}
		provider.mu.Lock()
		provider.revision = strings.TrimSpace(string(revision))
		provider.mu.Unlock()
		previousGeneration := child.batch.Workflow.Generation
		follow := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, target: "batch", tool: "followup_task", callID: "next-review", input: orchestrateSpawnInput{Message: "Review the same scope once more."}, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(follow)
		pump(func() bool { return len(follow.reply) > 0 })
		if result := <-follow.reply; result.err != nil {
			t.Fatal(result.err)
		}
		pump(func() bool {
			return child.batch.Workflow.Generation > previousGeneration && child.batch.Workflow.Outcome != nil && child.batch.Workflow.Outcome.State == orchestrate.DispatchAcknowledged
		})
		provider.mu.Lock()
		providerCompactions := provider.compactions
		provider.mu.Unlock()
		if providerCompactions != 0 {
			t.Fatal("review reset invoked provider compaction", providerCompactions)
		}
		if resetMode == "unavailable" {
			if child.batch.Workflow.Phase == orchestrate.PhaseReady || !workflowTerminal(child.batch.Workflow) {
				t.Fatal("unavailable local reset did not block workflow")
			}
			resetRejected := false
			for _, d := range child.batch.Workflow.Dispatches {
				if d.Generation == child.batch.Workflow.Generation && d.Kind == orchestrate.DispatchCorrectness {
					resetRejected = d.Compaction != nil && d.State == orchestrate.DispatchUncertain && d.TurnID == "" && d.ResultItemID == ""
				}
			}
			if !resetRejected || !strings.Contains(child.batch.Workflow.Reason, "Context reset unavailable") {
				t.Fatal("missing rejected reset or review ran after unavailable local reset", child.batch.Workflow.Reason)
			}
			return
		}
		if child.batch.Workflow.Phase != orchestrate.PhaseReady {
			t.Fatal("post-budget review failed", child.batch.Workflow.Reason, u.notice)
		}
		threads, resets := map[string]bool{}, 0
		for _, d := range child.batch.Workflow.Dispatches {
			if d.Kind != orchestrate.DispatchCorrectness && d.Kind != orchestrate.DispatchSimplification {
				continue
			}
			threads[d.ThreadID] = true
			if reviewResumes[d.ThreadID] != 1 {
				t.Fatal("reviewer must resume once on the fresh host, then use direct turns", reviewResumes)
			}
			if d.Compaction != nil {
				if d.Compaction.TurnID == "" || d.Compaction.ItemID == "" || d.Compaction.TurnID == d.TurnID || d.ResultItemID == "" {
					t.Fatal("compaction lost exact attribution", d)
				}
				resets++
			}
		}
		if len(threads) != 2 || resets != 2 || len(child.batch.Workflow.Reviews) != 2 {
			t.Fatal("review threads replaced or budget not compacted", threads, resets)
		}
	}
	if !configured {
		testOrchestrateArchiveNativeCodex(t, u, pump)
	}
	pump(func() bool { return len(u.orchestrateJobs) == 0 && len(u.orchestrateRequests) == 0 })

}
