package router

import (
	"encoding/json/jsontext"
	"path/filepath"
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
	call := func(tool string, args any, meta mcp.Meta, wantError bool) {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args, Meta: meta})
		if err != nil || result.IsError != wantError {
			t.Fatalf("%s = %+v, %v; want error %v", tool, result, err, wantError)
		}
	}
	meta := mcp.Meta{"threadId": "main", "sessionId": "host-session", "callId": "prepare",
		codexTurnMetadataHeader: map[string]any{"thread_id": "main", "turn_id": "turn"}}
	call("prepare", map[string]any{"task_name": "batch"}, meta, false)
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
	if _, _, err := store.BeginLaunch(ctx, workspace, "main", "batch", "work", jsontext.Value(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordThread(ctx, workspace, "main", "batch", "batch-thread", jsontext.Value(`{}`)); err != nil {
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
	manifests, err := filepath.Glob(filepath.Join(store.Directory, "*", "*.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatal("nested preparation created run state", manifests, err)
	}
}
