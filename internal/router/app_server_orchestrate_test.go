package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateMCPLaunch(t *testing.T) {
	for _, vcs := range []string{"git", "shadow", "svn"} {
		t.Run(vcs, func(t *testing.T) { testAppServerOrchestrateMCPLaunch(t, vcs) })
	}
}

func testAppServerOrchestrateMCPLaunch(t *testing.T, vcs string) {
	workspace := orchestrateVCSWorkspace(t, vcs)
	if vcs == "git" {
		module := orchestrateVCSWorkspace(t, "git")
		gitTestRun(t, workspace, "-c", "protocol.file.allow=always", "submodule", "add", "-q", module, "module")
		gitTestCommit(t, workspace)
		writeTestFile(t, filepath.Join(workspace, "module", "file"), "dirty source")
	}
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
	t.Cleanup(func() { u.closeOrchestratedViews(); u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.ctx, u.proxy = t.Context(), proxy
	u.session.start("main", workspace)
	u.model, u.reasoningEffort = "model", "high"
	u.statusConfig.Provider, u.statusConfig.ApprovalsReviewer = "mekugi_wrap", "user"
	u.statusConfig.Approval = []byte(`"never"`)
	u.statusConfig.PermissionProfile = []byte(`{"id":":workspace"}`)
	u.statusConfig.WorkspaceRoots = []string{workspace}
	proxy.orchestration.active.Store(true)
	meta := mcp.Meta{"threadId": "main", "sessionId": "session", "callId": "call", "itemId": "host-item", codexTurnMetadataHeader: map[string]any{"thread_id": "main", "turn_id": "main-turn"}}
	type callResult struct {
		value *mcp.CallToolResult
		err   error
	}
	callSeq := 0
	callContext := func(callCtx context.Context, tool string, args any) <-chan callResult {
		t.Helper()
		if tool == "spawn_agent" {
			args.(map[string]any)["kind"] = "diagnosis"
		}
		result := make(chan callResult, 1)
		callMeta := meta
		if tool == "wait_agent" {
			callSeq++
			callMeta = maps.Clone(meta)
			callMeta["callId"] = fmt.Sprint("wait-", callSeq)
		}
		go func() {
			value, err := client.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: args, Meta: callMeta})
			result <- callResult{value, err}
		}()
		return result
	}
	call := func(tool string, args any) <-chan callResult { return callContext(t.Context(), tool, args) }
	take := func(result <-chan callResult, wantError bool) *mcp.CallToolResult {
		t.Helper()
		for {
			select {
			case command := <-u.orchestrateCommands():
				u.startOrchestratedChild(command)
				drainOrchestrateWork(t, u)
			case got := <-result:
				if (got.err != nil || got.value.IsError) != wantError {
					t.Fatalf("MCP result: %+v, %v", got.value, got.err)
				}
				return got.value
			case <-time.After(5 * time.Second):
				t.Fatal("MCP result timeout")
			}
		}
	}
	assertEvent := func(result *mcp.CallToolResult, kind, status string) {
		t.Helper()
		var event orchestrateEvent
		encoded, err := json.Marshal(result.StructuredContent)
		var waitResult orchestrate.Wait
		_ = json.Unmarshal(encoded, &waitResult)
		encoded = waitResult.Event
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
	evidenceSource := filepath.Join(t.TempDir(), "image.png")
	evidenceBytes := []byte{0, 255, 13, 10, 128}
	if err := os.WriteFile(evidenceSource, evidenceBytes, 0600); err != nil {
		t.Fatal(err)
	}
	prepared := take(call("prepare", map[string]any{"task_name": "batch", "evidence": []orchestrate.EvidenceInput{{Name: "image.png", Source: evidenceSource}}}), false)
	var preparedBatch orchestrate.Batch
	preparedJSON, err := json.Marshal(prepared.StructuredContent)
	if err != nil || json.Unmarshal(preparedJSON, &preparedBatch) != nil || len(preparedBatch.Evidence) != 1 {
		t.Fatal("MCP preparation lost evidence paths", prepared, err)
	}
	if err := os.Remove(evidenceSource); err != nil {
		t.Fatal(err)
	}
	take(call("prepare", map[string]any{"task_name": "batch", "evidence": []orchestrate.EvidenceInput{{Name: "image.png", Source: evidenceSource}}}), false)
	result := call("wait_agent", map[string]any{"timeout_ms": 1500000, "targets": []string{"batch"}})
	dispatch()
	registered := take(result, false)
	var registeredWait orchestrate.Wait
	if err := json.Unmarshal(mustMarshalJSON(registered.StructuredContent), &registeredWait); err != nil || registeredWait.ID == "" {
		t.Fatal(registered, err)
	}
	// Cancel the logical subscription before continuing the independent launch checks.
	cancelResult := call("wait_agent", map[string]any{"cancel_id": registeredWait.ID})
	dispatch()
	take(cancelResult, false)
	take(call("wait_agent", map[string]any{"timeout_ms": 0}), true)
	batches, err := store.List(ctx, workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	batch := batches[0]
	if _, err := proxy.applyJournal(ctx, workspace, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate batch"), Agent: "/root/batch", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	sink := proxy.journals.attachNative(workspace, "main")
	defer proxy.journals.detachNative(sink)
	if err := proxy.journals.restoreNative(ctx, replay, sink); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(batch.Cwd, "input"), "copied input")
	if err := os.WriteFile(preparedBatch.Evidence[0].Path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	rejected := call("spawn_agent", map[string]any{"task_name": "batch", "message": "work"})
	dispatch()
	take(rejected, true)
	if w.Len() != 0 {
		t.Fatal("changed evidence dispatched a host effect", w.String())
	}
	if err := os.WriteFile(preparedBatch.Evidence[0].Path, evidenceBytes, 0600); err != nil {
		t.Fatal(err)
	}
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "work"})
	dispatch()
	request := btwTestRequest(t, w, "thread/start", "")
	batches, _ = store.List(ctx, workspace, "main")
	if batches[0].State != "prepared" || batches[0].Launch == nil {
		t.Fatal("host dispatch preceded intent")
	}
	response, _ := json.Marshal(map[string]any{"sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never", "thread": map[string]any{"id": "child", "cwd": batch.Cwd}, "model": "effective-model", "reasoningEffort": "high"})
	orchestrateTestReply(t, u, request, string(response))
	sink.mu.Lock()
	mounted := sink.mounted
	sink.mu.Unlock()
	if mounted == nil || !slices.ContainsFunc(mounted.Items, func(item journalItem) bool { return item.Agent == "/root/batch" && item.Path == "/1/@child" }) {
		t.Fatal("confirmed thread linkage did not refresh Main before the first turn")
	}
	request = btwTestRequest(t, w, "turn/start", "child")
	if !strings.Contains(request.Params.Input[0].Text, preparedBatch.Evidence[0].Path) {
		t.Fatal("first child turn lost the retained evidence handoff", request.Params.Input)
	}
	if got, err := os.ReadFile(preparedBatch.Evidence[0].Path); err != nil || !slices.Equal(got, evidenceBytes) {
		t.Fatal("child handoff lacks original evidence bytes", got, err)
	}
	if vcs == "git" {
		if got, err := os.ReadFile(filepath.Join(batch.Cwd, "module", "file")); err != nil || string(got) != "base" {
			t.Fatal("first child turn lacks committed submodule input", err)
		}
	}
	batches, _ = store.List(ctx, workspace, "main")
	if len(request.Params.Input) != 1 || strings.Count(request.Params.Input[0].Text, "Your coordinator is main.") != 1 || !strings.HasSuffix(request.Params.Input[0].Text, batches[0].Launch.Message) {
		t.Fatal("first child input lost its coordinator or assignment", request.Params.Input)
	}
	if batches[0].Launch.ThreadID != "child" || batches[0].State != "prepared" {
		t.Fatal("turn dispatched before retained thread identity")
	}
	orchestrateTestReply(t, u, request, `{"turn":{"id":"child-turn"}}`)
	take(result, false)
	if u.thread != "main" || u.turn != "" || u.model != "model" {
		t.Fatal("child launch replaced Main")
	}
	childView := u.navigation.views["child"]
	if childView == nil || childView.model != "effective-model" || childView.session.cwd != batch.Cwd {
		t.Fatal("confirmed launch did not retain its own view")
	}
	if got, err := os.ReadFile(filepath.Join(batch.Cwd, "input")); err != nil || string(got) != "copied input" {
		t.Fatal("prepared input changed", err)
	}
	journalServerWire, journalClientWire := mcp.NewInMemoryTransports()
	journalServer, err := newJournalMCPServer(proxy).Connect(t.Context(), journalServerWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journalServer.Close()
	journalClient, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), journalClientWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journalClient.Close()
	blockArgs := map[string]any{"mutations": []any{map[string]any{"op": "add", "kind": "task", "title": "Batch decision", "state": "blocked", "reason": "Choose the target"}}}
	blockMeta := mcp.Meta{"threadId": "child", "sessionId": "session", "callId": "blocked", "itemId": "journal-item", codexTurnMetadataHeader: map[string]any{"thread_id": "child", "turn_id": "child-turn"}}
	if result, err := journalClient.CallTool(t.Context(), &mcp.CallToolParams{Name: "journal_mutate", Arguments: blockArgs, Meta: blockMeta}); err != nil || result.IsError {
		t.Fatal("child journal blocker", result, err)
	}
	if err := u.tickOrchestratedViews(u); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	blockWait := call("wait_agent", map[string]any{})
	dispatch()
	blocked := take(blockWait, false)
	assertEvent(blocked, "blocked", "blocked")
	var blockEvent orchestrateEvent
	var blockedWait orchestrate.Wait
	_ = json.Unmarshal(mustMarshalJSON(blocked.StructuredContent), &blockedWait)
	encodedBlock := blockedWait.Event
	if err := json.Unmarshal(encodedBlock, &blockEvent); err != nil || blockEvent.Path != "/1" || blockEvent.Message != "Choose the target" || w.Len() != 0 {
		t.Fatal("journal blocker lost identity or dispatched a turn", blockEvent, err)
	}
	// Adapter launch and journal wiring are covered above for each source.
	// Messaging and cancellation use the same host lifecycle for all adapters.
	if vcs != "git" {
		return
	}
	if quit, err := u.key(3); quit || err != nil {
		t.Fatal("running child was abandoned", err)
	}
	appServerTestKeys(t, u, "/clear\r")
	if w.Len() != 0 || !u.sessionBusy() {
		t.Fatal("session replacement can strand the child")
	}
	u.draft = ""
	messageCall := func(callCtx context.Context, tool, from, target, message, id string) <-chan callResult {
		meta := mcp.Meta{"threadId": from, "sessionId": "session", "callId": id, codexTurnMetadataHeader: map[string]any{"thread_id": from, "turn_id": from + "-turn"}}
		result := make(chan callResult, 1)
		go func() {
			value, err := client.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"target": target, "message": message}, Meta: meta})
			result <- callResult{value, err}
		}()
		return result
	}
	followup := func(callCtx context.Context, from, target, message, id string) <-chan callResult {
		return messageCall(callCtx, "followup_task", from, target, message, id)
	}
	assertDelivery := func(result *mcp.CallToolResult, state, turn string) {
		t.Helper()
		var d orchestrate.Delivery
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil || json.Unmarshal(encoded, &d) != nil || d.State != state || d.TurnID != turn {
			t.Fatalf("delivery: %s, %v", encoded, err)
		}
	}
	u.orchestrateThreads["child"].turn = ""
	for _, text := range []string{"first deferred", "second deferred"} {
		for range 2 {
			result = messageCall(t.Context(), "send_message", "main", "batch", text, text)
			dispatch()
			assertDelivery(take(result, false), "queued", "")
		}
	}
	if w.Len() != 0 {
		t.Fatal("deferred message woke or steered its recipient")
	}
	result = messageCall(t.Context(), "send_message", "main", "batch", "changed", "first deferred")
	dispatch()
	take(result, true)
	u.orchestrateThreads["child"].turn = "child-turn"
	result = messageCall(t.Context(), "send_message", "main", "batch", "correction", "running-message")
	dispatch()
	messageRequest := btwTestRequest(t, w, "turn/steer", "child")
	if len(messageRequest.Params.Input) != 1 || messageRequest.Params.Input[0].Text != "correction" {
		t.Fatal("running message", messageRequest)
	}
	orchestrateTestReply(t, u, messageRequest, `{"turnId":"child-turn"}`)
	assertDelivery(take(result, false), "delivered", "child-turn")
	result = followup(t.Context(), "main", "batch", "continue", "running")
	dispatch()
	steers := appServerTurnRequests(t, w)
	if len(steers) != 1 || steers[0].Method != "turn/steer" || steers[0].Params.ExpectedTurnID != "child-turn" || steers[0].text() != "continue" {
		t.Fatal("running follow-up", steers)
	}
	select {
	case <-result:
		t.Fatal("delivery returned before host acknowledgement")
	default:
	}
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"child-turn"}}`, steers[0].ID))
	assertDelivery(take(result, false), "delivered", "child-turn")
	firstFollowup := followup(t.Context(), "main", "batch", "first queued follow-up", "serial-first")
	dispatch()
	firstRequest := btwTestRequest(t, w, "turn/steer", "child")
	secondFollowup := followup(t.Context(), "main", "batch", "second queued follow-up", "serial-second")
	dispatch()
	if w.Len() != 0 {
		t.Fatal("second follow-up dispatched before the first acknowledgement")
	}
	if len(firstRequest.Params.Input) != 1 || firstRequest.Params.Input[0].Text != "first queued follow-up" {
		t.Fatal("first follow-up input changed", firstRequest)
	}
	orchestrateTestReply(t, u, firstRequest, `{"turnId":"child-turn"}`)
	assertDelivery(take(firstFollowup, false), "delivered", "child-turn")
	secondRequest := btwTestRequest(t, w, "turn/steer", "child")
	if len(secondRequest.Params.Input) != 1 || secondRequest.Params.Input[0].Text != "second queued follow-up" {
		t.Fatal("second follow-up input changed", secondRequest)
	}
	orchestrateTestReply(t, u, secondRequest, `{"turnId":"child-turn"}`)
	assertDelivery(take(secondFollowup, false), "delivered", "child-turn")
	result = followup(t.Context(), "main", "batch", "continue", "running")
	dispatch()
	assertDelivery(take(result, false), "delivered", "child-turn")
	if w.Len() != 0 {
		t.Fatal("repeat delivered twice")
	}
	result = followup(t.Context(), "main", "batch", "changed", "running")
	dispatch()
	take(result, true)
	result = followup(t.Context(), "main", "batch", "rejected", "rejected-followup")
	dispatch()
	request = btwTestRequest(t, w, "turn/steer", "child")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"turn changed"}}`, request.ID))
	take(result, true)
	if u.orchestrateThreads["child"].turn != "child-turn" || u.orchestrateThreads["child"].batch.Launch.TurnID != "child-turn" {
		t.Fatal("follow-up rejection replaced batch lifecycle")
	}
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "work"})
	dispatch()
	take(result, false)
	if w.Len() != 0 {
		t.Fatal("repeat dispatched another thread")
	}
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "changed"})
	dispatch()
	take(result, true)
	result = call("interrupt_agent", map[string]any{"target": "/root/batch"})
	dispatch()
	request = btwTestRequest(t, w, "turn/interrupt", "child")
	if request.Params.TurnID != "child-turn" {
		t.Fatal("interrupt targeted another turn")
	}
	orchestrateTestReply(t, u, request, `{}`)
	take(result, false)

	result = call("interrupt_agent", map[string]any{"target": "batch"})
	dispatch()
	request = btwTestRequest(t, w, "turn/interrupt", "child")
	event := appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"interrupted"}}`)}
	if err := u.message(event); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	wait := call("wait_agent", map[string]any{})
	dispatch()
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
	// Explicit cancellation retires the subscription without consuming prompts.
	wait = call("wait_agent", map[string]any{})
	dispatch()
	var subscription orchestrate.Wait
	_ = json.Unmarshal(mustMarshalJSON(take(wait, false).StructuredContent), &subscription)
	wait = call("wait_agent", map[string]any{"cancel_id": subscription.ID})
	dispatch()
	take(wait, false)
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
	var asyncWait orchestrate.Wait
	_ = json.Unmarshal(mustMarshalJSON(async.StructuredContent), &asyncWait)
	encoded := asyncWait.Event
	if err := json.Unmarshal(encoded, &asyncEvent); err != nil || asyncEvent.Item != "async-question" || len(asyncEvent.Request) != 0 {
		t.Fatal("asynchronous question identity lost", asyncEvent, err)
	}
	result = followup(t.Context(), "main", "/root/batch", "resume", "idle")
	dispatch()
	request = btwTestRequest(t, w, "turn/start", "child")
	if len(request.Params.Input) != 1 || request.Params.Input[0].Text != "first deferred\nsecond deferred\nresume" {
		t.Fatal("deferred messages did not accompany the next turn in order", request)
	}
	// Completion can precede the turn/start response.
	u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"followup-turn","status":"completed"}}`)})
	orchestrateTestReply(t, u, request, `{"turn":{"id":"followup-turn"}}`)
	orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
	assertDelivery(take(result, false), "delivered", "followup-turn")
	result = messageCall(t.Context(), "send_message", "main", "batch", "correction", "running-message")
	dispatch()
	assertDelivery(take(result, false), "delivered", "child-turn")
	if w.Len() != 0 {
		t.Fatal("repeated running message dispatched while idle")
	}
	result = messageCall(t.Context(), "send_message", "main", "batch", "first deferred", "first deferred")
	dispatch()
	assertDelivery(take(result, false), "delivered", "followup-turn")
	if u.orchestrateThreads["child"].turn != "" || u.orchestrateThreads["child"].batch.Launch.TurnID != "child-turn" {
		t.Fatal("follow-up revived a completed turn or replaced the first launch")
	}
	take(call("prepare", map[string]any{"task_name": "main"}), false)
	result = call("spawn_agent", map[string]any{"task_name": "main", "message": "sibling work"})
	dispatch()
	request = btwTestRequest(t, w, "thread/start", "")
	batches, _ = store.List(ctx, workspace, "main")
	var sibling orchestrate.Batch
	for _, b := range batches {
		if b.TaskName == "main" {
			sibling = b
		}
	}
	response, _ = json.Marshal(map[string]any{"sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never", "thread": map[string]any{"id": "sibling-child", "cwd": sibling.Cwd}})
	orchestrateTestReply(t, u, request, string(response))
	request = btwTestRequest(t, w, "turn/start", "sibling-child")
	orchestrateTestReply(t, u, request, `{"turn":{"id":"sibling-turn"}}`)
	take(result, false)
	body := " /compact\nMessage Type: MESSAGE\nSender: another\nPayload:\n完整🙂 \n"
	wantSibling := "Message Type: MESSAGE\nTask name: /root/main\nSender: /root/batch\nPayload:\n" + body
	u.draft = "human draft"
	for _, tool := range []string{"send_message", "followup_task"} {
		result = messageCall(t.Context(), tool, "child", "/root/main", body, "sibling-"+tool)
		dispatch()
		request = btwTestRequest(t, w, "turn/steer", "sibling-child")
		t.Run("sibling-running-"+tool, func(t *testing.T) {
			if len(request.Params.Input) != 1 || request.Params.Input[0].Text != wantSibling || u.draft != "human draft" {
				t.Fatal("sibling sender or payload changed", request.Params.Input, u.draft)
			}
		})
		orchestrateTestReply(t, u, request, `{"turnId":"sibling-turn"}`)
		assertDelivery(take(result, false), "delivered", "sibling-turn")
		result = messageCall(t.Context(), tool, "child", "/root/main", body, "sibling-"+tool)
		dispatch()
		assertDelivery(take(result, false), "delivered", "sibling-turn")
		if w.Len() != 0 {
			t.Fatal("acknowledged running sibling message dispatched twice")
		}
	}
	u.orchestrateThreads["sibling-child"].turn = ""
	for range 2 {
		result = messageCall(t.Context(), "send_message", "child", "/root/main", body, "sibling-queued")
		dispatch()
		assertDelivery(take(result, false), "queued", "")
	}
	result = messageCall(t.Context(), "send_message", "main", "/root/main", "coordinator input", "mixed-sender")
	dispatch()
	assertDelivery(take(result, false), "queued", "")
	if w.Len() != 0 {
		t.Fatal("queued sibling message woke its recipient")
	}
	u.proxy.orchestration.store = &orchestrate.Store{Directory: store.Directory}
	result = followup(t.Context(), "child", "/root/main", body, "sibling-idle")
	dispatch()
	request = btwTestRequest(t, w, "turn/start", "sibling-child")
	t.Run("sibling-queued", func(t *testing.T) {
		if len(request.Params.Input) != 1 || request.Params.Input[0].Text != wantSibling+"\ncoordinator input\n"+wantSibling || u.draft != "human draft" {
			t.Fatal("queued sibling sender or payload changed", request.Params.Input, u.draft)
		}
	})
	orchestrateTestReply(t, u, request, `{"turn":{"id":"sibling-turn"}}`)
	assertDelivery(take(result, false), "delivered", "sibling-turn")
	result = messageCall(t.Context(), "send_message", "child", "/root/main", body, "sibling-queued")
	dispatch()
	assertDelivery(take(result, false), "delivered", "sibling-turn")
	if w.Len() != 0 {
		t.Fatal("acknowledged sibling message dispatched twice")
	}
	u.draft = ""
	queued := messageCall(t.Context(), "send_message", "child", "main", "first Main input", "main-queue")
	dispatch()
	assertDelivery(take(queued, false), "reported", "")
	queued = messageCall(t.Context(), "send_message", "sibling-child", "main", "second Main input", "main-queue-sibling")
	dispatch()
	assertDelivery(take(queued, false), "reported", "")
	if w.Len() != 0 {
		t.Fatal("deferred Main input woke the coordinator")
	}
	result = followup(t.Context(), "child", "main", "coordinator wake", "main-alias")
	dispatch()
	assertDelivery(take(result, false), "reported", "")
	queued = messageCall(t.Context(), "send_message", "child", "main", "first Main input", "main-queue")
	dispatch()
	assertDelivery(take(queued, false), "reported", "")
	if w.Len() != 0 {
		t.Fatal("child reports dispatched coordinator input")
	}
	// A native subagent does not acquire run-member authority by sharing cwd.
	nativeCtx, nativeRelease, err := replay.beginSession(ctx, "native", "session")
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.initialize(nativeCtx, replay, workspace, "native", "/root/native", "main"); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(nativeCtx, replay, workspace, "native", "main", "/root/native", true); err != nil {
		t.Fatal(err)
	}
	nativeRelease()
	result = followup(t.Context(), "native", "batch", "not a run member", "native-call")
	dispatch()
	take(result, true)
	lateCtx, cancelLate := context.WithCancel(t.Context())
	result = followup(lateCtx, "main", "/root/main", "late acknowledgement", "late-followup")
	lateCommand := dispatch()
	request = btwTestRequest(t, w, "turn/steer", "sibling-child")
	cancelLate()
	select {
	case <-lateCommand.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up cancellation not propagated")
	}
	take(result, true)
	orchestrateTestReply(t, u, request, `{"turnId":"sibling-turn"}`)
	if w.Len() != 0 || u.orchestrateThreads["sibling-child"].turn != "sibling-turn" {
		t.Fatal("late follow-up interrupted recipient work")
	}
	preCtx, cancelPre := context.WithCancel(t.Context())
	result = followup(preCtx, "main", "/root/main", "cancel before UI dispatch", "pre-followup")
	select {
	case c := <-u.orchestrateCommands():
		u.startOrchestratedChild(c)
		cancelPre()
		select {
		case <-c.ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("pre-dispatch cancellation not propagated")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up command timeout")
	}
	drainOrchestrateWork(t, u)
	take(result, true)
	if w.Len() != 0 {
		t.Fatal("canceled follow-up reached the host")
	}
	// Removed options reject at the real MCP schema, before dispatch.
	for _, option := range []string{"fork_turns"} {
		take(call("spawn_agent", map[string]any{"task_name": "batch", "message": "work", option: "all"}), true)
	}
	// The MCP schema admits the configured role selector before retained-input checks.
	result = call("spawn_agent", map[string]any{"task_name": "batch", "message": "work", "agent_type": "custom-role"})
	if selected := dispatch(); selected.input.AgentType != "custom-role" {
		t.Fatal("MCP lost the role selector")
	}
	take(result, true) // An existing launch cannot change its role.
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
		response, _ = json.Marshal(map[string]any{"sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never", "thread": map[string]any{"id": phase + "-child", "cwd": current.Cwd}})
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
