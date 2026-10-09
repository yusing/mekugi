package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateMCPLaunch(t *testing.T) {
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "file"), "base")
	gitTestCommit(t, workspace)
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := replay.beginSession(t.Context(), "main", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	proxy := &mekugiProxy{journals: newJournalStore(), replayStore: replay}
	if err := proxy.journals.initialize(ctx, replay, workspace, "main", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(ctx, replay, workspace, "main", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	store := &orchestrate.Store{Directory: t.TempDir()}
	serverWire, clientWire := mcp.NewInMemoryTransports()
	server, err := newOrchestrateMCPServer(proxy, store).Connect(t.Context(), serverWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u, w := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.ctx, u.proxy = t.Context(), proxy
	u.session.start("main", workspace)
	u.model, u.reasoningEffort = "model", "high"
	u.statusConfig.Provider, u.statusConfig.ApprovalsReviewer = "mekugi_wrap", "user"
	u.statusConfig.Approval = []byte(`"never"`)
	u.statusConfig.PermissionProfile = []byte(`{"id":":workspace"}`)
	u.statusConfig.WorkspaceRoots = []string{workspace}
	proxy.orchestration.active.Store(true)
	meta := mcp.Meta{"threadId": "main", "sessionId": "session", "callId": "call", codexTurnMetadataHeader: map[string]any{"thread_id": "main", "turn_id": "main-turn"}}
	type callResult struct {
		value *mcp.CallToolResult
		err   error
	}
	callContext := func(callCtx context.Context, tool string, args any) <-chan callResult {
		t.Helper()
		result := make(chan callResult, 1)
		go func() {
			value, err := client.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: args, Meta: meta})
			result <- callResult{value, err}
		}()
		return result
	}
	call := func(tool string, args any) <-chan callResult { return callContext(t.Context(), tool, args) }
	take := func(result <-chan callResult, wantError bool) *mcp.CallToolResult {
		t.Helper()
		select {
		case got := <-result:
			if (got.err != nil || got.value.IsError) != wantError {
				t.Fatalf("MCP result: %+v, %v", got.value, got.err)
			}
			return got.value
		case <-time.After(5 * time.Second):
			t.Fatal("MCP result timeout")
		}
		return nil
	}
	assertEvent := func(result *mcp.CallToolResult, kind, status string) {
		t.Helper()
		var event orchestrateEvent
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil || json.Unmarshal(encoded, &event) != nil || event.Task != "batch" || event.Thread != "child" || event.Kind != kind || event.Status != status {
			t.Fatalf("wait event: %s, %v", encoded, err)
		}
	}
	dispatch := func() *orchestrateCommand {
		t.Helper()
		select {
		case command := <-u.orchestrateCommands():
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			return command
		case <-time.After(5 * time.Second):
			t.Fatal("UI command timeout")
			return nil
		}
	}
	take(call("prepare", map[string]any{"task_name": "batch"}), false)
	result := call("wait_agent", map[string]any{"timeout_ms": 1})
	// The timeout may expire before the UI receives this command.
	select {
	case command := <-u.orchestrateCommands():
		u.startOrchestratedChild(command)
	case <-time.After(10 * time.Millisecond):
	}
	timedOut := take(result, false)
	if encoded, _ := json.Marshal(timedOut.StructuredContent); string(encoded) != `{"timed_out":true}` {
		t.Fatalf("timeout: %s", encoded)
	}
	take(call("wait_agent", map[string]any{"timeout_ms": 0}), true)
	batches, err := store.List(ctx, workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	batch := batches[0]
	writeTestFile(t, filepath.Join(batch.Cwd, "input"), "copied input")
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "work"})
	dispatch()
	request := btwTestRequest(t, w, "thread/start", "")
	batches, _ = store.List(ctx, workspace, "main")
	if batches[0].State != "starting" || batches[0].Launch == nil {
		t.Fatal("host dispatch preceded intent")
	}
	response, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "child", "cwd": batch.Cwd}, "model": "effective-model", "reasoningEffort": "high"})
	orchestrateTestReply(t, u, request, string(response))
	request = btwTestRequest(t, w, "turn/start", "child")
	batches, _ = store.List(ctx, workspace, "main")
	if batches[0].Launch.ThreadID != "child" || batches[0].State != "started" {
		t.Fatal("turn dispatched before retained thread identity")
	}
	orchestrateTestReply(t, u, request, `{"turn":{"id":"child-turn"}}`)
	take(result, false)
	if u.thread != "main" || u.turn != "" || u.model != "model" {
		t.Fatal("child launch replaced Main")
	}
	if got, err := os.ReadFile(filepath.Join(batch.Cwd, "input")); err != nil || string(got) != "copied input" {
		t.Fatal("prepared input changed", err)
	}
	if quit, err := u.key(3); quit || err != nil {
		t.Fatal("running child was abandoned", err)
	}
	appServerTestKeys(t, u, "/clear\r")
	if w.Len() != 0 || !u.sessionBusy() {
		t.Fatal("session replacement can strand the child")
	}
	u.draft = ""
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "work"})
	dispatch()
	take(result, false)
	if w.Len() != 0 {
		t.Fatal("repeat dispatched another thread")
	}
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "changed"})
	dispatch()
	take(result, true)
	wait := call("wait_agent", map[string]any{"timeout_ms": 5000})
	dispatch()
	result = call("interrupt_agent", map[string]any{"target": "/root/batch"})
	dispatch()
	request = btwTestRequest(t, w, "turn/interrupt", "child")
	if request.Params.TurnID != "child-turn" {
		t.Fatal("interrupt targeted another turn")
	}
	orchestrateTestReply(t, u, request, `{}`)
	take(result, false)
	select {
	case <-wait:
		t.Fatal("interrupt acknowledgement completed lifecycle wait")
	default:
	}
	result = call("interrupt_agent", map[string]any{"target": "batch"})
	dispatch()
	request = btwTestRequest(t, w, "turn/interrupt", "child")
	event := appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"interrupted"}}`)}
	u.orchestrateMessage(event)
	drainOrchestrateWork(t, u)
	assertEvent(take(wait, false), "done", "interrupted")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"turn already finished"}}`, request.ID))
	take(result, true)
	batches, _ = store.List(ctx, workspace, "main")
	if batches[0].Launch.HostStatus != "interrupted" {
		t.Fatal("interrupt error replaced the host outcome")
	}
	if u.orchestrateBusy() {
		t.Fatal("completion left child running")
	}
	// Cancellation retires a wait, not the next observed prompt.
	waitCtx, cancelWait := context.WithCancel(t.Context())
	wait = callContext(waitCtx, "wait_agent", map[string]any{})
	waitCommand := dispatch()
	cancelWait()
	select {
	case <-waitCommand.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("wait cancellation not propagated")
	}
	take(wait, true)
	for _, prompt := range []struct{ method, kind string }{{"item/tool/requestUserInput", "question"}, {"item/commandExecution/requestApproval", "approval"}} {
		message := appserver.Message{ID: []byte(`"prompt"`), Method: prompt.method, Params: []byte(`{"threadId":"child","turnId":"question-turn"}`)}
		if u.orchestrateMessage(message) {
			t.Fatal("wait observer consumed a host prompt")
		}
		wait = call("wait_agent", map[string]any{})
		dispatch()
		assertEvent(take(wait, false), prompt.kind, "")
	}
	u.orchestrateMessage(appserver.Message{Method: "item/completed", Params: []byte(`{"threadId":"child","turnId":"question-turn","item":{"id":"async-question","type":"agentMessage","delivery":"async","questions":[{"title":"Which target?"}]}}`)})
	wait = call("wait_agent", map[string]any{})
	dispatch()
	async := take(wait, false)
	assertEvent(async, "question", "")
	var asyncEvent orchestrateEvent
	encoded, _ := json.Marshal(async.StructuredContent)
	if err := json.Unmarshal(encoded, &asyncEvent); err != nil || asyncEvent.Item != "async-question" || len(asyncEvent.Request) != 0 {
		t.Fatal("asynchronous question identity lost", asyncEvent, err)
	}
	// Removed options reject at the real MCP schema, before dispatch.
	for _, option := range []string{"fork_turns", "agent_type"} {
		take(call("spawn_agent", map[string]any{"task_name": "batch", "message": "work", option: "all"}), true)
	}
	// A canceled caller's late thread acknowledgement is retained without a turn.
	for _, phase := range []string{"thread", "turn", "persist", "rejected"} {
		take(call("prepare", map[string]any{"task_name": phase}), false)
		cancelCtx, cancel := context.WithCancel(t.Context())
		result = callContext(cancelCtx, "spawn_agent", map[string]any{"task_name": phase, "message": "work"})
		command := dispatch()
		cancelCall := func() {
			cancel()
			select {
			case <-command.ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("MCP cancellation did not reach dispatch")
			}
		}
		request = btwTestRequest(t, w, "thread/start", "")
		if phase == "rejected" {
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"denied"}}`, request.ID))
			take(result, true)
			cancel()
			continue
		}
		var current orchestrate.Batch
		batches, _ = store.List(ctx, workspace, "main")
		for _, b := range batches {
			if b.TaskName == phase {
				current = b
			}
		}
		if phase == "thread" {
			cancelCall()
		}
		response, _ = json.Marshal(map[string]any{"thread": map[string]any{"id": phase + "-child", "cwd": current.Cwd}})
		orchestrateTestReply(t, u, request, string(response))
		if phase == "turn" || phase == "persist" {
			request = btwTestRequest(t, w, "turn/start", phase+"-child")
			cancelCall()
			directory := store.Directory
			if phase == "persist" {
				store.Directory = filepath.Join(t.TempDir(), "unavailable")
				writeTestFile(t, store.Directory, "block storage")
			}
			orchestrateTestReply(t, u, request, `{"turn":{"id":"late-turn"}}`)
			request = btwTestRequest(t, w, "turn/interrupt", phase+"-child")
			if u.orchestrateThreads[phase+"-child"].turn != "late-turn" {
				t.Fatal("lost observed live turn")
			}
			store.Directory = directory
			orchestrateTestReply(t, u, request, `{}`)
		} else if w.Len() != 0 {
			t.Fatal("canceled start dispatched a turn")
		}
		take(result, true)
		batches, _ = store.List(ctx, workspace, "main")
		for _, b := range batches {
			if b.TaskName == phase && b.Launch.ThreadID != phase+"-child" {
				t.Fatal("late acknowledgement lost")
			}
		}
	}
}
