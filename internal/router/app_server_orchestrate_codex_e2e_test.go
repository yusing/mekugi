//go:build journal_e2e

package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestrateCodexProvider struct {
	native       appServerChildMetadataProvider
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
	p.native.mu.Unlock()
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
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`, "-c", `approval_policy="never"`, "-c", `default_permissions=":workspace"`, "-c", "include_collaboration_mode_instructions=false")
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
	store := &orchestrate.Store{Directory: t.TempDir()}
	proxy.orchestration = &orchestrateRuntime{store: store}
	batch, err := store.Prepare(ctx, workspace, main, "batch")
	if err != nil {
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
	if params.Permissions != ":workspace" || params.ApprovalPolicy != "never" || len(params.RuntimeWorkspaceRoots) == 0 || params.RuntimeWorkspaceRoots[0] != batch.Cwd {
		t.Fatalf("inherited permissions missing: %+v", params)
	}
	pump(func() bool { return len(provider.nativeOutput) > 0 })
	if output := <-provider.nativeOutput; !strings.Contains(output, batch.Cwd) {
		t.Fatal("native command escaped prepared checkout", output)
	}
	nativeScoped := false
	for thread, path := range u.session.paths {
		if thread != main && thread != result.batch.Launch.ThreadID && strings.HasPrefix(path, "/orchestrate/"+result.batch.Launch.ThreadID+"/") {
			nativeScoped = true
		}
	}
	if !nativeScoped {
		t.Fatal("native descendant presentation escaped batch identity", u.session.paths)
	}
	first := <-provider.requests
	if !strings.Contains(string(first), command.input.Message) {
		t.Fatal("fresh assignment missing at provider")
	}
	interrupt := &orchestrateCommand{ctx: ctx, workspace: workspace, main: main, target: "batch", reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(interrupt)
	pump(func() bool {
		return len(interrupt.reply) > 0 && u.orchestrateThreads[result.batch.Launch.ThreadID].turn == "" && len(u.orchestrateJobs) == 0
	})
	if r := <-interrupt.reply; r.err != nil {
		t.Fatal(r.err)
	}
	batches, err := store.List(ctx, workspace, main)
	if err != nil {
		t.Fatal(err)
	}
	if batches[0].Launch.HostStatus != "interrupted" {
		t.Fatal("host interruption not retained", batches[0])
	}
}
