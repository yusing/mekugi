package router

import (
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestJournalOrchestrateDiagnosisCompletionMCP(t *testing.T) {
	for _, vcs := range []string{"git", "shadow"} {
		t.Run(vcs, func(t *testing.T) {
			replay, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u, launch := orchestratePendingWorkflow(t, replay, orchestrateVCSWorkspace(t, vcs), nil, &orchestrateSpawnInput{TaskName: "batch", Message: "diagnose only", Kind: orchestrate.WorkflowDiagnosis})
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"diagnose"}}`)
			p, workspace := u.proxy, u.session.cwd
			b := u.orchestrateThreads["child"].batch
			if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Resolve report"), State: new("working")},
				{Op: "add", Under: "/1", Kind: "task", Title: new("Review diagnosis"), State: new("working"), Agent: "/root/batch"},
			}); err != nil {
				t.Fatal(err)
			}
			serverWire, clientWire := mcp.NewInMemoryTransports()
			server, err := newJournalMCPServer(p).Connect(t.Context(), serverWire, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientWire, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			call := func(id, state string, fail bool) {
				t.Helper()
				result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "journal_mutate", Arguments: map[string]any{"mutations": []any{
					map[string]any{"op": "set", "p": "/1/1", "state": state},
					map[string]any{"op": "set", "p": "/1", "state": "done"},
					map[string]any{"op": "finish"},
				}}, Meta: mcp.Meta{"threadId": "main", "sessionId": "session", "itemId": id, "callId": id,
					codexTurnMetadataHeader: map[string]any{"thread_id": "main", "turn_id": "main-turn"}}})
				if err != nil || result.IsError != fail {
					t.Fatalf("%s: %s, %v; want error %v", id, mustMarshalJSON(result), err, fail)
				}
			}
			call("active", "done", true)
			workflowCompleted(t, u, "child", "diagnose", "Diagnosis report complete; no changes")
			w := u.client.Input.(*appServerTestInput)
			orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
			for _, err := range []error{
				p.journals.initialize(t.Context(), replay, b.Cwd, "native", "/root/worker", ""),
				p.journals.bindIdentity(t.Context(), replay, b.Cwd, "native", "child", "/root/worker", true),
				p.journals.observeLifecycle(t.Context(), replay, b.Cwd, "native", "working", ""),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			call("active_native", "done", true)
			if err := p.journals.observeLifecycle(t.Context(), replay, b.Cwd, "native", "blocked", "host failed"); err != nil {
				t.Fatal(err)
			}
			call("failed_native", "done", true)
			if err := p.journals.observeLifecycle(t.Context(), replay, b.Cwd, "native", "done", ""); err != nil {
				t.Fatal(err)
			}
			// Retained diagnosis completion must work without live parent or child state.
			p.replayStore, err = openMekugiReplayStore(replay.directory)
			if err != nil {
				t.Fatal(err)
			}
			p.journals = newJournalStore()
			call("accept", "accepted", true)
			call("complete", "done", false)
			p.journals = newJournalStore()
			call("complete", "done", false) // Replay the successful receipt.
			nodes, err := p.journals.readTree(u.ctx, p.replayStore, workspace, "main", "", "", nil, "own")
			if err != nil || nodes[0].State != "done" || nodes[0].Children[0].State != "done" || nodes[0].Children[0].Integration != nil {
				t.Fatal("diagnosis completion did not survive restart", nodes, err)
			}
			summary, err := summaryForTest(t, u.ctx, p.replayStore, workspace, "main")
			if err != nil || !strings.Contains(summary.Text, "Resume: no runnable local task") {
				t.Fatal("diagnosis kept continuation open", summary, err)
			}
			orchestrateTargetMCPClient(t, u, "cleanup")("main", true)
			batches, err := p.orchestration.store.Snapshot(workspace, "main")
			if err != nil {
				t.Fatal(err)
			}
			retained := batches[0]
			if retained.Integration != nil || retained.Archive != nil || retained.Workflow.Candidate != nil || retained.Workflow.Phase != orchestrate.PhaseImplementation || len(retained.Workflow.Dispatches) != 0 {
				t.Fatal("diagnosis fabricated integration or review state", retained)
			}
			if _, err := os.Stat(b.Checkout); err != nil || submoduleCleanupGit(t, b.Checkout, "rev-parse", "HEAD") != b.Base || submoduleCleanupGit(t, b.Checkout, "status", "--porcelain") != "" || w.Len() != 0 {
				t.Fatal("diagnosis changed checkout or dispatched host work", err, w.String())
			}
		})
	}
}
