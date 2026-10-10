//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestrateCodexProvider struct {
	native       appServerChildMetadataProvider
	promptCalls  int
	requests     chan []byte
	nativeOutput chan string
}

func (p *orchestrateCodexProvider) forwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*http.Response, error) {
	p.requests <- append([]byte(nil), body...)
	metadata, _ := decodeCodexTurnMetadata(headers)
	if metadata.SubagentKind != "" {
		thread := headers.Get(threadIDHeader)
		p.native.mu.Lock()
		p.native.turns[thread]++
		turn := p.native.turns[thread]
		p.native.mu.Unlock()
		if turn == 1 {
			return mchangesNestedCodexResponse(turn, map[string]any{"type": "custom_tool_call", "id": "native-pwd", "call_id": "native-pwd", "name": "exec", "input": `text(await tools.exec_command({cmd:"pwd",max_output_tokens:100}));`, "status": "completed"}), nil
		}
		var request struct {
			Input []struct {
				Type   string         `json:"type"`
				Output jsontext.Value `json:"output"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		for _, item := range request.Input {
			if item.Type == "function_call_output" || item.Type == "custom_tool_call_output" {
				p.nativeOutput <- string(item.Output)
			}
		}
		return routerFaultCodexSuccessResponse(), nil
	}
	thread := headers.Get(threadIDHeader)
	p.native.mu.Lock()
	p.native.turns[thread]++
	turn := p.native.turns[thread]
	prompt := 0
	if strings.Contains(string(body), "Start the idle follow-up probe.") {
		p.promptCalls++
		prompt = p.promptCalls
	}
	p.native.mu.Unlock()
	if prompt == 1 || prompt == 2 {
		name, args := "request_user_input_async", map[string]any{"questions": []any{map[string]any{"title": "Who receives the batch?", "options": []string{"Customers", "Internal"}}}}
		if prompt == 2 {
			name, args = "request_user_input", map[string]any{"questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Which batch scope?", "options": []any{map[string]any{"label": "Narrow", "description": "Only this batch"}, map[string]any{"label": "Broad", "description": "All paths"}}}}}
		}
		return mchangesNestedCodexResponse(turn, map[string]any{"type": "function_call", "id": name, "call_id": name, "name": name, "arguments": string(mustMarshalJSON(args)), "status": "completed"}), nil
	}
	if prompt == 3 {
		return mchangesNestedCodexResponse(turn, map[string]any{"type": "custom_tool_call", "id": "approval-probe", "call_id": "approval-probe", "name": "exec", "input": `text(await tools.exec_command({cmd:"printf approved",sandbox_permissions:"require_escalated",justification:"Fixture command approval"}));`, "status": "completed"}), nil
	}
	if prompt >= 4 {
		return routerFaultCodexSuccessResponse(), nil
	}
	if turn >= 3 {
		<-responseCtx.Done()
		return nil, responseCtx.Err()
	}
	name, args := "spawn_agent", map[string]any{"task_name": "metadata_probe", "fork_turns": "none", "message": "Report the working directory through stock execution and complete."}
	if turn == 2 {
		name, args = "wait_agent", map[string]any{"timeout_ms": 10000}
	}
	return mchangesNestedCodexResponse(turn, map[string]any{"type": "function_call", "id": "native-parent-" + strconv.Itoa(turn), "call_id": "native-parent-" + strconv.Itoa(turn), "namespace": "collaboration", "name": name, "arguments": string(mustMarshalJSON(args)), "status": "completed"}), nil
}

func TestAppServerOrchestrateNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	workspace := gitTestWorkspace(t)
	writeTestFile(t, workspace+"/file", "baseline")
	gitTestCommit(t, workspace)
	provider := &orchestrateCodexProvider{native: appServerChildMetadataProvider{turns: make(map[string]int)}, requests: make(chan []byte, 32), nativeOutput: make(chan string, 4)}
	proxy := newManagedMekugiProxy(t)
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server",
		"-c", `model_providers.orchestrate_test={name="orchestrate_test",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
		"-c", `model_provider="orchestrate_test"`, "-c", `model="gpt-6-astra"`, "-c", `model_reasoning_effort="high"`,
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`, "-c", `approval_policy="on-request"`, "-c", `default_permissions=":workspace"`, "-c", "features.default_mode_request_user_input=true", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env, cmd.Dir = routerFaultCodexEnvironment(t), workspace
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := &appServerUI{ctx: ctx, client: client, proxy: proxy, view: newLiveActivityView(), agents: newLiveActivityView(), requests: make(map[string]string), approvalMode: true, notifications: &nativeNotifications{out: io.Discard}, resumeCwd: workspace}
	u.ensureShell()
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	defer u.closeOrchestrateStorage()
	defer u.closeOrchestratedViews()
	pump := func(done func() bool) {
		t.Helper()
		for !done() {
			select {
			case m, ok := <-client.Messages:
				if !ok {
					t.Fatal("host closed")
				}
				if err := u.message(m); err != nil {
					t.Fatal(err)
				}
			case complete := <-u.orchestrateCompletions:
				u.completeOrchestrateWork(complete)
			case <-ctx.Done():
				provider.native.mu.Lock()
				detail := make(map[string]int)
				for k, v := range provider.native.turns {
					detail[k] = v
				}
				provider.native.mu.Unlock()
				t.Fatalf("host timeout: %v; notice=%s paths=%v reads=%v metadata=%+v calls=%v", ctx.Err(), u.notice, u.session.paths, u.session.metadata, u.session.threads, detail)
			}
		}
	}
	if err := u.request("initialize", nil); err != nil {
		t.Fatal(err)
	}
	pump(func() bool {
		return u.thread != "" && !u.modelsLoading && u.statusConfig.PermissionProfile.Kind() == '{'
	})
	main := u.thread
	// This fixture dispatches UI commands directly, without a Main provider turn
	// that would normally initialize the journal before its MCP call.
	ctx, releaseMain, err := proxy.replayStore.beginSession(ctx, main, "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseMain()
	if err := proxy.journals.initialize(ctx, proxy.replayStore, workspace, main, "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(ctx, proxy.replayStore, workspace, main, "", "/root", true); err != nil {
		t.Fatal(err)
	}
	store := &orchestrate.Store{Directory: t.TempDir()}
	proxy.orchestration = &orchestrateRuntime{store: store}
	batch, err := store.Prepare(ctx, workspace, main, "batch")
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(t.TempDir(), "evidence.txt")
	writeTestFile(t, evidence, "retained input")
	batch, err = store.RetainEvidence(ctx, workspace, main, "batch", []orchestrate.EvidenceInput{{Name: "evidence.txt", Source: evidence}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	tier := "priority"
	command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, input: orchestrateSpawnInput{TaskName: "batch", Message: "Spawn the assigned native probe and wait for it.", ServiceTier: &tier}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(command)
	pump(func() bool { return len(command.reply) > 0 })
	result := <-command.reply
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.batch.Launch.ThreadID == main || u.thread != main {
		t.Fatal("batch replaced or forked main")
	}
	var effective struct {
		Model           string              `json:"model"`
		ReasoningEffort string              `json:"reasoningEffort"`
		ServiceTier     string              `json:"serviceTier"`
		Thread          appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(result.batch.Launch.ThreadResult, &effective); err != nil {
		t.Fatal(err)
	}
	if effective.Model != "gpt-6-astra" || effective.ReasoningEffort != "high" || effective.ServiceTier != "priority" || effective.Thread.Cwd != batch.Cwd {
		t.Fatalf("effective launch settings: %+v", effective)
	}
	var params struct {
		Permissions           string   `json:"permissions"`
		RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots"`
		ApprovalPolicy        string   `json:"approvalPolicy"`
	}
	if err := json.Unmarshal(result.batch.Launch.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Permissions != ":workspace" || params.ApprovalPolicy != "on-request" || len(params.RuntimeWorkspaceRoots) == 0 || params.RuntimeWorkspaceRoots[0] != batch.Cwd {
		t.Fatalf("inherited permissions missing: %+v", params)
	}
	pump(func() bool { return len(provider.nativeOutput) > 0 })
	if output := <-provider.nativeOutput; !strings.Contains(output, batch.Cwd) {
		t.Fatal("native command escaped prepared checkout", output)
	}
	foundEvidence := false
	for len(provider.requests) != 0 {
		body := <-provider.requests
		foundEvidence = foundEvidence || bytes.Contains(body, []byte(batch.Evidence[0].Path))
	}
	if !foundEvidence {
		t.Fatal("installed host lost retained evidence from the child input")
	}
	nativeScoped := false
	nativeThread := ""
	for thread, path := range u.session.paths {
		if thread != main && thread != result.batch.Launch.ThreadID && strings.HasPrefix(path, "/orchestrate/"+result.batch.Launch.ThreadID+"/") {
			nativeScoped = true
			nativeThread = thread
		}
	}
	if !nativeScoped {
		t.Fatal("native descendant presentation escaped batch identity", u.session.paths)
	}
	nodes, err := proxy.readJournalTree(ctx, workspace, main, "", "", nil, "combined")
	if err != nil {
		t.Fatal(err)
	}
	path := "/@agents/@" + journalPointerKey(result.batch.Launch.ThreadID)
	if node, ok := mountFind(nodes, path); !ok || node.Agent != "/root/batch" {
		t.Fatal("installed thread did not mount in Main's journal", node)
	}
	if node, ok := mountFind(nodes, path+"/@agents/@"+journalPointerKey(nativeThread)); !ok || node.Agent != "/root/batch/metadata_probe" {
		t.Fatal("installed native descendant lost cross-checkout journal ancestry", node)
	}
	view := u.navigation.views[result.batch.Launch.ThreadID]
	if view == nil || view.session.paths[nativeThread] != "/root/metadata_probe" || view.model != effective.Model {
		t.Fatal("installed host tree did not populate its independent view")
	}
	u.draft, view.draft = "Main draft", "Batch draft"
	for _, key := range []byte{2, ']'} {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.viewedUI() != view || u.thread != main {
		t.Fatal("installed navigation replaced the execution coordinator")
	}
	var frame bytes.Buffer
	if err := view.paint(&frame, 120, 40); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frame.String(), "main › batch") || !strings.Contains(frame.String(), "Batch draft") {
		t.Fatal("installed batch view did not render its breadcrumb and composer")
	}
	first := <-provider.requests
	if !strings.Contains(string(first), command.input.Message) {
		t.Fatal("fresh assignment missing at provider")
	}
	queued := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, target: "batch", followup: true, deferred: true, callID: "installed-queued", input: orchestrateSpawnInput{Message: "Deferred input for the next probe turn."}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(queued)
	pump(func() bool { return len(queued.reply) > 0 })
	if r := <-queued.reply; r.err != nil || r.delivery == nil || r.delivery.State != "queued" {
		t.Fatalf("installed deferred message: %+v", r)
	}
	followup := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, target: "batch", followup: true, callID: "installed-running", input: orchestrateSpawnInput{Message: "Continue the probe when ready."}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(followup)
	pump(func() bool { return len(followup.reply) > 0 })
	if r := <-followup.reply; r.err != nil || r.delivery == nil || r.delivery.State != "delivered" || r.delivery.TurnID != u.orchestrateThreads[result.batch.Launch.ThreadID].turn {
		t.Fatalf("installed running follow-up: %+v", r)
	}
	interrupt := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, target: "batch", reply: make(chan orchestrateResult, 1)}
	wait := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, wait: true, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(wait)
	u.startOrchestratedChild(interrupt)
	pump(func() bool {
		return len(interrupt.reply) > 0 && len(wait.reply) > 0 && u.orchestrateThreads[result.batch.Launch.ThreadID].turn == "" && len(u.orchestrateJobs) == 0
	})
	if r := <-interrupt.reply; r.err != nil {
		t.Fatal(r.err)
	}
	if r := <-wait.reply; r.err != nil || r.event == nil || r.event.Kind != "done" || r.event.Status != "interrupted" || r.event.Thread != result.batch.Launch.ThreadID {
		t.Fatalf("installed host wait: %+v", r)
	}
	batches, err := store.List(ctx, workspace, main)
	if err != nil {
		t.Fatal(err)
	}
	if batches[0].Launch.HostStatus != "interrupted" {
		t.Fatal("host interruption not retained", batches[0])
	}
	followup = &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, target: "batch", followup: true, callID: "installed-idle", input: orchestrateSpawnInput{Message: "Start the idle follow-up probe."}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(followup)
	pump(func() bool { return len(followup.reply) > 0 })
	if r := <-followup.reply; r.err != nil || r.delivery == nil || r.delivery.State != "delivered" || r.delivery.TurnID == batches[0].Launch.TurnID {
		t.Fatalf("installed idle follow-up: %+v", r)
	}
	observed := false
	for !observed {
		select {
		case body := <-provider.requests:
			if strings.Contains(string(body), followup.input.Message) {
				if !strings.Contains(string(body), queued.input.Message) {
					t.Fatal("queued input missing from installed next turn")
				}
				observed = true
			}
		case <-ctx.Done():
			t.Fatal("idle follow-up did not reach the provider", ctx.Err())
		}
	}
	// Real host prompts from the batch appear while Main stays selected. Sync
	// answers and approval responses preserve the host IDs; the later async
	// answer uses the batch's ordinary composer lifecycle after its turn ends.
	u.switchOrchestratedThread(main)
	pump(func() bool { return view.questionCount() == 2 })
	u.openQuestions()
	questionTestPaint(t, u, 80)
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	pump(func() bool { return len(view.approvals.pending) > 0 })
	u.openApprovals()
	questionTestPaint(t, u, 80)
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	pump(func() bool { return view.turn == "" && view.questionCount() == 1 })
	u.openQuestions()
	questionTestPaint(t, u, 80)
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	pump(func() bool { return view.questionCount() == 0 && view.turn == "" && !view.starting() })
	if u.draft != "Main draft" || view.draft != "Batch draft" || u.viewedUI() != u {
		t.Fatal("installed prompt handling changed viewed drafts or coordinator")
	}
	asyncSeen, syncSeen, approvalSeen := false, false, false
	for len(provider.requests) > 0 {
		body := string(<-provider.requests)
		asyncSeen = asyncSeen || strings.Contains(body, "<send_user_message_question_reply>") && strings.Contains(body, "Customers")
		var request struct {
			Input []struct {
				CallID string         `json:"call_id"`
				Output jsontext.Value `json:"output"`
			} `json:"input"`
		}
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		for _, item := range request.Input {
			output := string(item.Output)
			if item.CallID == "request_user_input" {
				syncSeen = syncSeen || strings.Contains(output, "Narrow")
			}
			if item.CallID == "approval-probe" {
				approvalSeen = approvalSeen || strings.Contains(output, `\"output\":\"approved\"`) && strings.Contains(output, `\"exit_code\":0`)
				if !approvalSeen {
					t.Logf("approval output: %s", output)
				}
			}
		}
	}
	if !asyncSeen || !syncSeen || !approvalSeen {
		t.Fatalf("installed prompts missing provider evidence: async=%v sync=%v approval=%v", asyncSeen, syncSeen, approvalSeen)
	}
}
