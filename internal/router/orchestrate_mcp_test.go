package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateMCP(t *testing.T) {
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "file"), "base")
	gitTestCommit(t, workspace)
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := replay.beginSession(t.Context(), "main", "host-session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	journal := newJournalStore()
	if err := journal.initialize(ctx, replay, workspace, "main", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := journal.bindIdentity(ctx, replay, workspace, "main", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{journals: journal, replayStore: replay}
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
	u, wire := newAppServerTestUI()
	u.ctx, u.proxy = ctx, proxy
	u.session.start("main", workspace)
	proxy.orchestration.active.Store(true)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, description := range map[string]string{
		"spawn_agent":   "Similar to mekugi_collaboration.spawn_agent",
		"wait_agent":    "Only a selected event or explicit timeout wakes Main",
		"send_message":  "Report only a new blocker requiring action or one final handoff per assignment",
		"followup_task": "Report only a new blocker requiring action or one final handoff per assignment",
	} {
		index := slices.IndexFunc(listed.Tools, func(tool *mcp.Tool) bool { return tool.Name == name })
		if index < 0 || !strings.Contains(listed.Tools[index].Description, description) {
			t.Fatalf("%s discovery lacks tool guidance", name)
		}
	}
	waitIndex := slices.IndexFunc(listed.Tools, func(tool *mcp.Tool) bool { return tool.Name == "wait_agent" })
	if waitIndex < 0 {
		t.Fatal("wait tool missing from MCP discovery")
	}
	var waitSchema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(mustMarshalJSON(listed.Tools[waitIndex].InputSchema), &waitSchema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waitSchema.Properties["timeout_ms"].Description, "no timeout wake") ||
		!strings.Contains(waitSchema.Properties["targets"].Description, "all (default)") ||
		strings.Contains(orchestrateCoordinatorInstructions, "wait_agent:") {
		t.Fatal("wait parameter help must be exposed by MCP, not duplicated in coordinator instructions")
	}
	call := func(tool string, args any, meta mcp.Meta, wantError bool) *mcp.CallToolResult {
		t.Helper()
		var result *mcp.CallToolResult
		var err error
		done := make(chan struct{})
		go func() {
			result, err = client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args, Meta: meta})
			close(done)
		}()
		if tool == "prepare" {
			select {
			case command := <-u.orchestrateCommands():
				u.startOrchestratedChild(command)
				drainOrchestrateWork(t, u)
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("prepare admission timed out")
			}
		}
		<-done
		if err != nil || result.IsError != wantError {
			t.Fatalf("%s = %s, %v; want error %v", tool, mustMarshalJSON(result), err, wantError)
		}
		return result
	}
	meta := mcp.Meta{"threadId": "main", "sessionId": "host-session", "callId": "prepare", "itemId": "host-item",
		codexTurnMetadataHeader: map[string]any{"thread_id": "main", "turn_id": "turn"}}
	call("prepare", map[string]any{"task_name": "batch"}, meta, false)
	received := make(chan *orchestrateCommand, 1)
	for _, field := range []string{"review_waiver", "review_scope"} {
		go func() {
			command := <-proxy.orchestration.commands
			received <- command
			command.reply <- orchestrateResult{delivery: &orchestrate.Delivery{State: "delivered"}}
		}()
		call("followup_task", map[string]any{"target": "batch", "message": "finish", field: "explicit coordinator input"}, meta, false)
		command := <-received
		got := map[string]string{"review_waiver": command.reviewWaiver, "review_scope": command.reviewScope}
		if got[field] != "explicit coordinator input" || command.main != "main" || command.workspace != workspace {
			t.Fatal("authenticated coordinator input was not dispatched", command)
		}
	}
	// Proven roots include runtime reviewers and /btw forks. Neither is
	// the session coordinator, even when its journal has no retained run.
	for _, thread := range []string{"reviewer", "side-thread", "fork"} {
		rootCtx, release, err := replay.beginSession(t.Context(), thread, "host-session")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		fork := ""
		if thread == "fork" {
			fork = "main"
		}
		if err := journal.initialize(rootCtx, replay, workspace, thread, "/root", fork); err != nil {
			t.Fatal(err)
		}
		if err := journal.bindIdentity(rootCtx, replay, workspace, thread, "", "/root", true); err != nil {
			t.Fatal(err)
		}
		result := call("prepare", map[string]any{"task_name": "unauthorized"}, mcp.Meta{"threadId": thread, "sessionId": "host-session", "callId": "prepare", "itemId": "host-item",
			codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "turn"}}, true)
		if !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "active coordinator") {
			t.Fatal("proven root did not reach coordinator admission", result)
		}
		if _, err := store.Snapshot(workspace, thread); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("non-coordinator created run state", thread, err)
		}
	}
	proxy.orchestration.active.Store(false)
	call("prepare", map[string]any{"task_name": "inactive"}, meta, true)
	proxy.orchestration.active.Store(true)
	preparedRecovery, err := replay.postCompactContext(ctx, workspace, "main")
	if err != nil || strings.Count(preparedRecovery, orchestrateCoordinatorInstructions) != 1 {
		t.Fatal("prepared run did not retain coordinator instructions", err, preparedRecovery)
	}
	listedAgents := call("list_agents", map[string]any{}, meta, false)
	var summary struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(mustMarshalJSON(listedAgents.StructuredContent), &summary); err != nil || len(summary.Agents) != 1 || summary.Agents[0]["task_name"] != "batch" || len(summary.Agents[0]) != 2 {
		t.Fatal("default listing did not return a compact prepared batch", summary, err)
	}
	call("list_agents", map[string]any{}, nil, true)
	call("prepare", map[string]any{"task_name": "no_identity"}, nil, true)
	call("prepare", map[string]any{"task_name": "no_turn"}, mcp.Meta{"threadId": "main", "sessionId": "host-session"}, true)
	call("prepare", map[string]any{"task_name": "escape", "workspace": t.TempDir()}, meta, true)
	if err := journal.initialize(ctx, replay, workspace, "child", "/root/native", "main"); err != nil {
		t.Fatal(err)
	}
	if err := journal.bindIdentity(ctx, replay, workspace, "child", "main", "/root/native", true); err != nil {
		t.Fatal(err)
	}
	call("prepare", map[string]any{"task_name": "native"}, meta, true)
	call("prepare", map[string]any{"task_name": "nested"}, mcp.Meta{"threadId": "child", "sessionId": "host-session", "callId": "child-prepare",
		codexTurnMetadataHeader: map[string]any{"thread_id": "child", "turn_id": "child-turn"}}, true)
	// A confirmed independent batch root is not another coordinator. Its
	// admission survives reopening storage with no live parent/controller.
	b, err := store.Prepare(ctx, workspace, "main", "batch")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureWorkflow(ctx, workspace, "main", "batch", orchestrate.WorkflowAssignment{Kind: orchestrate.WorkflowImplementation, Scope: "list regression"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginLaunch(ctx, workspace, "main", "batch", "work", jsontext.Value(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordThread(ctx, workspace, "main", "batch", "batch-thread"); err != nil {
		t.Fatal(err)
	}
	// Resume clears the workflow reason, but an older uncertain host effect
	// continues to block cleanup and must remain visible in every listing view.
	for _, err := range []error{
		store.RecordTurn(ctx, workspace, "main", "batch", "batch-thread", "initial"),
		store.AdmitCandidate(ctx, workspace, "main", "batch", orchestrate.Candidate{Revision: b.Base}, "report"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, dispatch, err := store.BeginWorkflowDispatch(ctx, workspace, "main", "batch", "lost", 1, orchestrate.DispatchCorrectness); err != nil || !dispatch {
		t.Fatal("review dispatch", dispatch, err)
	}
	for _, err := range []error{
		store.FailWorkflowDispatch(ctx, workspace, "main", "batch", "lost", true, "connection lost"),
		store.StopWorkflow(ctx, workspace, "main", "batch", orchestrate.PhaseNeedsDecision, "connection lost"),
		store.ResumeWorkflow(ctx, workspace, "main", "batch"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	run := journalRun{Directory: store.Directory, Workspace: workspace, Main: "main"}
	childCtx, childRelease, err := replay.beginSession(t.Context(), "batch-thread", "host-session")
	if err != nil {
		t.Fatal(err)
	}
	defer childRelease()
	workerCtx, workerRelease, err := replay.beginSession(t.Context(), "worker", "host-session")
	if err != nil {
		t.Fatal(err)
	}
	defer workerRelease()
	for _, err := range []error{
		journal.bindRun(ctx, replay, workspace, "main", run),
		journal.initialize(childCtx, replay, b.Cwd, "batch-thread", "/root", ""),
		journal.bindIdentity(childCtx, replay, b.Cwd, "batch-thread", "", "/root", true),
		journal.bindRun(childCtx, replay, b.Cwd, "batch-thread", run),
		journal.initialize(workerCtx, replay, b.Cwd, "worker", "/root/worker", "batch-thread"),
		journal.bindIdentity(workerCtx, replay, b.Cwd, "worker", "batch-thread", "/root/worker", true),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	proxy.replayStore, err = openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore()
	for _, thread := range []string{"batch-thread", "worker"} {
		u.thread = thread
		u.session.start(thread, b.Cwd)
		for _, field := range []string{"review_waiver", "review_scope"} {
			denied := call("followup_task", map[string]any{"target": "batch", "message": "finish", field: "replace authority"}, mcp.Meta{"threadId": thread, "sessionId": "resumed-session", "callId": "amend",
				codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "resumed-turn"}}, true)
			if reason := string(mustMarshalJSON(denied.Content)); !strings.Contains(reason, "coordinator") && !strings.Contains(reason, "proven main thread") {
				t.Fatal("amendment failed outside coordinator authorization", reason)
			}
		}
		call("list_agents", map[string]any{"detail": "full"}, mcp.Meta{"threadId": thread, "sessionId": "resumed-session"}, true)
		call("prepare", map[string]any{"task_name": "nested"}, mcp.Meta{"threadId": thread, "sessionId": "resumed-session", "callId": "nested",
			codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "resumed-turn"}}, true)
	}
	u.thread = "main"
	u.session.start("main", workspace)
	call("prepare", map[string]any{"task_name": "other"}, meta, false)
	call("send_message", map[string]any{"target": "batch", "message": "finish", "review_waiver": "skip reviews"}, meta, true)
	call("send_message", map[string]any{"target": "batch", "message": "finish", "review_scope": "different scope"}, meta, true)
	// Selection keeps run-wide unconfirmed deliveries, even from another batch.
	if _, err := store.RegisterWait(ctx, workspace, "main", orchestrate.Wait{ID: "wait", Targets: []string{"other"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitEvent := mustMarshalJSON(map[string]any{"task": "other", "kind": "completed", "message": strings.Repeat("implementer report and review evidence ", 2000)})
	if _, _, err := store.AdvanceWaits(ctx, workspace, "main", []orchestrate.WaitEvent{{ID: "event", Task: "other", Data: jsontext.Value(waitEvent)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWait(ctx, workspace, "main", "wait", "uncertain", "acknowledgement unavailable"); err != nil {
		t.Fatal(err)
	}
	before, err := store.List(ctx, workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	waitsBefore, err := store.Waits(workspace, "main", "uncertain", "rejected")
	if err != nil {
		t.Fatal(err)
	}
	for _, detail := range []string{"", "summary", "checks", "reviews", "full"} {
		args := map[string]any{"targets": []string{"/root/batch", "batch"}, "detail": detail}
		wantAgents := 1
		if detail == "" {
			args, wantAgents = map[string]any{}, 2
		}
		result := call("list_agents", args, meta, false)
		var selected struct {
			Agents []orchestrate.Batch `json:"agents"`
			Waits  []orchestrate.Wait  `json:"unsettled_waits"`
		}
		if err := json.Unmarshal(mustMarshalJSON(result.StructuredContent), &selected); err != nil || len(selected.Agents) != wantAgents || selected.Agents[0].TaskName != "batch" || len(selected.Waits) != 1 || selected.Waits[0].State != "uncertain" || selected.Waits[0].EventID != "event" || selected.Waits[0].ID != "wait" || selected.Waits[0].Error != "acknowledgement unavailable" {
			t.Fatal("selection or unconfirmed wait lost", selected, err)
		}
		if detail == "full" {
			if string(selected.Waits[0].Event) != string(waitEvent) {
				t.Fatal("full listing lost exact wait payload")
			}
		} else if len(selected.Waits[0].Event) != 0 || len(mustMarshalJSON(result.StructuredContent)) > 2000 {
			t.Fatal("compact listing includes unsolicited wait evidence", detail)
		}
		w := selected.Agents[0].Workflow
		if w == nil || w.Generation != 2 || w.Reason != "" || len(w.Dispatches) != 1 || w.Dispatches[0].Generation != 1 || w.Dispatches[0].Reason != "connection lost" || !workflowUnsettled(selected.Agents[0]) {
			t.Fatal("resumed workflow hid an unsettled dispatch", detail, w)
		}
	}
	call("list_agents", map[string]any{"targets": "all"}, meta, false)
	after, err := store.List(ctx, workspace, "main")
	if err != nil || string(mustMarshalJSON(before)) != string(mustMarshalJSON(after)) {
		t.Fatal("listing changed retained batches", err)
	}
	waitsAfter, err := store.Waits(workspace, "main", "uncertain", "rejected")
	if err != nil || string(mustMarshalJSON(waitsBefore)) != string(mustMarshalJSON(waitsAfter)) {
		t.Fatal("listing changed wait delivery", err)
	}
	// Selecting an independent fork as Main authorizes its own run, not the
	// original Main's run. Admission does not depend on the thread's name.
	u.thread = "fork"
	u.session.start("fork", workspace)
	forkMeta := mcp.Meta{"threadId": "fork", "sessionId": "resumed-session", "callId": "prepare", "itemId": "host-item",
		codexTurnMetadataHeader: map[string]any{"thread_id": "fork", "turn_id": "turn"}}
	call("prepare", map[string]any{"task_name": "independent"}, forkMeta, false)
	u.thread = "main"
	u.session.start("main", workspace)
	if wire.Len() != 0 {
		t.Fatal("preparation started a host thread or turn", wire.String())
	}
	// Exercise the exposed union schema and retained-run admission before UI dispatch.
	for _, targets := range []any{nil, "batch", []string{}, []string{""}, []string{"/root/batch/native"}, []any{1}, []string{"unknown"}, []string{"native"}} {
		call("list_agents", map[string]any{"targets": targets}, meta, true)
		result := call("wait_agent", map[string]any{"targets": targets}, meta, true)
		if names, ok := targets.([]string); ok && slices.Contains(names, "unknown") && !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "list_agents") {
			t.Fatal("unknown target did not explain retained-run discovery", result)
		}
	}
	call("list_agents", map[string]any{"detail": "invalid"}, meta, true)
	call("list_agents", map[string]any{"workspace": t.TempDir()}, meta, true)
	for _, args := range []map[string]any{{"cancel_id": "wait", "targets": "all"}, {"cancel_id": "wait", "timeout_ms": 1}, {"timeout_ms": 1500001}, {"timeout_ms": 0}} {
		call("wait_agent", args, meta, true)
	}
	proxy.orchestration.active.Store(true)
	proxy.orchestration.commands = make(chan *orchestrateCommand, 1)
	for _, tc := range []struct {
		args     map[string]any
		want     []string
		deadline bool
	}{
		{map[string]any{}, nil, false},
		{map[string]any{"targets": "all"}, nil, false},
		{map[string]any{"targets": []string{"batch"}}, []string{"batch"}, false},
		{map[string]any{"targets": []string{"/root/batch", "other", "batch"}}, []string{"batch", "other"}, false},
		{map[string]any{"targets": []string{"batch"}, "timeout_ms": 1000}, []string{"batch"}, true},
	} {
		go func() {
			select {
			case command := <-proxy.orchestration.commands:
				event := orchestrateEvent{Kind: "message", Message: strings.Join(command.waitTargets, ",")}
				if command.waitTimeout != 0 {
					event.Status = "deadline"
				}
				command.reply <- orchestrateResult{wait: &orchestrate.Wait{Event: mustMarshalJSON(&event)}}
			case <-t.Context().Done():
			}
		}()
		result := call("wait_agent", tc.args, meta, false)
		var event orchestrateEvent
		var wait orchestrate.Wait
		_ = json.Unmarshal(mustMarshalJSON(result.StructuredContent), &wait)
		if err := json.Unmarshal(wait.Event, &event); err != nil || event.Message != strings.Join(tc.want, ",") {
			t.Fatal("wait selection was not passed to the UI", event, err)
		}
		if (event.Status == "deadline") != tc.deadline {
			t.Fatal("wait deadline did not match explicit timeout", tc.args, event)
		}
	}
	for _, caller := range []struct{ workspace, thread string }{
		{workspace, "child"}, {b.Cwd, "batch-thread"}, {b.Cwd, "worker"},
	} {
		if _, err := store.Snapshot(caller.workspace, caller.thread); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("nested preparation created run state", caller, err)
		}
	}
	if _, err := store.Prepare(ctx, workspace, "foreign-coordinator", "foreign_batch"); err != nil {
		t.Fatal(err)
	}
	call("wait_agent", map[string]any{"targets": []string{"foreign_batch"}}, meta, true)
	call("list_agents", map[string]any{"targets": []string{"foreign_batch"}, "detail": "full"}, meta, true)
}
