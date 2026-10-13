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
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, description := range map[string]string{
		"spawn_agent":   "Similar to mekugi_collaboration.spawn_agent",
		"wait_agent":    "Final answers reach Main automatically as separate notifications; waiting is optional",
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
	if !strings.Contains(waitSchema.Properties["timeout_ms"].Description, "no deadline") ||
		!strings.Contains(waitSchema.Properties["targets"].Description, "all (default)") ||
		strings.Contains(orchestrateCoordinatorInstructions, "wait_agent:") {
		t.Fatal("wait parameter help must be exposed by MCP, not duplicated in coordinator instructions")
	}
	call := func(tool string, args any, meta mcp.Meta, wantError bool) *mcp.CallToolResult {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args, Meta: meta})
		if err != nil || result.IsError != wantError {
			t.Fatalf("%s = %+v, %v; want error %v", tool, result, err, wantError)
		}
		return result
	}
	meta := mcp.Meta{"threadId": "main", "sessionId": "host-session", "callId": "prepare",
		codexTurnMetadataHeader: map[string]any{"thread_id": "main", "turn_id": "turn"}}
	call("prepare", map[string]any{"task_name": "batch"}, meta, false)
	preparedRecovery, err := replay.postCompactContext(ctx, workspace, "main")
	if err != nil || strings.Count(preparedRecovery, orchestrateCoordinatorInstructions) != 1 {
		t.Fatal("prepared run did not retain coordinator instructions", err, preparedRecovery)
	}
	call("list_agents", map[string]any{}, meta, false)
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
	if _, _, err := store.BeginLaunch(ctx, workspace, "main", "batch", "work", jsontext.Value(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordThread(ctx, workspace, "main", "batch", "batch-thread"); err != nil {
		t.Fatal(err)
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
		call("prepare", map[string]any{"task_name": "nested"}, mcp.Meta{"threadId": thread, "sessionId": "resumed-session", "callId": "nested",
			codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "resumed-turn"}}, true)
	}
	call("prepare", map[string]any{"task_name": "other"}, meta, false)
	// Exercise the exposed union schema and retained-run admission before UI dispatch.
	for _, targets := range []any{nil, "batch", []string{}, []string{""}, []string{"/root/batch/native"}, []any{1}, []string{"unknown"}, []string{"native"}} {
		result := call("wait_agent", map[string]any{"targets": targets}, meta, true)
		if names, ok := targets.([]string); ok && slices.Contains(names, "unknown") && !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "list_agents") {
			t.Fatal("unknown target did not explain retained-run discovery", result)
		}
	}
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
}
