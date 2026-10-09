package router

import (
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
	serverWire, clientWire := mcp.NewInMemoryTransports()
	server, err := newOrchestrateMCPServer(proxy, &orchestrate.Store{Directory: t.TempDir()}).Connect(t.Context(), serverWire, nil)
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
}
