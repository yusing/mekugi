package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestJournalOrchestrateAcceptanceMCP(t *testing.T) {
	for _, vcs := range []string{"git", "hg"} {
		t.Run(vcs, func(t *testing.T) { testJournalOrchestrateAcceptanceMCP(t, vcs) })
	}
}

func testJournalOrchestrateAcceptanceMCP(t *testing.T, vcs string) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := orchestrateVCSWorkspace(t, vcs)
	if vcs == "git" {
		writeTestFile(t, filepath.Join(source, "nested", "file"), "base")
		gitTestCommit(t, source)
		source = filepath.Join(source, "nested")
	}
	u, request := orchestrateIdentityPendingTurnInWorkspace(t, replay, source)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"initial"}}`)
	p, workspace := u.proxy, u.session.cwd
	childWorkspace := u.orchestrateThreads["child"].batch.Cwd
	childRoot := u.orchestrateThreads["child"].batch.Checkout
	if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Deliver run"), State: new("working")},
		{Op: "add", Under: "/1", Kind: "task", Title: new("Integrate batch"), State: new("working"), Agent: "/root/batch"},
	}); err != nil {
		t.Fatal(err)
	}
	childCtx, release, err := replay.beginSession(t.Context(), "child", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := p.applyJournal(childCtx, childWorkspace, "child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Result"), State: new("done")}}); err != nil {
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
	call := func(thread, id string, fail bool, ops ...map[string]any) {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "journal_mutate", Arguments: map[string]any{"mutations": ops}, Meta: mcp.Meta{
			"threadId": thread, "sessionId": "session", "itemId": "item", "callId": id,
			codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "turn"},
		}})
		if err != nil || result.IsError != fail {
			t.Fatalf("%s: %+v, %v; want error %v", id, result, err, fail)
		}
	}
	accept := map[string]any{"op": "set", "p": "/1/1", "state": "accepted"}
	call("child", "self_accept", true, map[string]any{"op": "set", "p": "/1", "state": "accepted"})
	call("main", "unbound_accept", true, map[string]any{"op": "set", "p": "/1", "state": "accepted"})
	// Runs saved before accepted existed can have a completed done binding.
	if err := p.journals.transaction(u.ctx, replay, workspace, "main", func(j *threadJournal, _ bool) error {
		j.Items[j.treeIndex("/1/1")].State = "done"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call("main", "legacy_running", true, map[string]any{"op": "set", "p": "/1/1", "state": "working"}, accept)
	call("main", "legacy_reopen", false, map[string]any{"op": "set", "p": "/1/1", "state": "working"})
	call("main", "active", true, accept)
	if err := p.journals.observeLifecycle(childCtx, replay, childWorkspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		p.journals.initialize(childCtx, replay, childWorkspace, "worker", "/root/worker", ""),
		p.journals.bindIdentity(childCtx, replay, childWorkspace, "worker", "child", "/root/worker", true),
		p.journals.observeLifecycle(childCtx, replay, childWorkspace, "worker", "working", ""),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	call("main", "active_native", true, accept)
	if err := p.journals.observeLifecycle(childCtx, replay, childWorkspace, "worker", "done", ""); err != nil {
		t.Fatal(err)
	}
	call("main", "bypass", true, map[string]any{"op": "set", "p": "/1/1", "state": "done"})
	call("main", "premature_parent", true, map[string]any{"op": "set", "p": "/1", "state": "done"})
	writeTestFile(t, filepath.Join(childWorkspace, "result"), "result")
	call("main", "dirty", true, accept)
	if vcs == "hg" {
		hgTestRun(t, childWorkspace, "add", "result")
		hgTestRun(t, childWorkspace, "commit", "-u", "test", "-m", "result")
	} else {
		gitTestCommit(t, childWorkspace)
	}
	call("main", "unintegrated", true, accept)
	// Native integration is external to the tool. Unrelated source edits survive.
	writeTestFile(t, filepath.Join(workspace, "file"), "staged source edit")
	if vcs == "git" {
		gitTestRun(t, workspace, "add", "file")
	}
	writeTestFile(t, filepath.Join(workspace, "file"), "unstaged source edit")
	if vcs == "hg" {
		hgTestRun(t, workspace, "update", "-r", u.orchestrateThreads["child"].batch.Branch)
	} else {
		command := exec.CommandContext(t.Context(), "git", "-c", "core.hooksPath="+os.DevNull, "-C", workspace, "merge", "--ff-only", u.orchestrateThreads["child"].batch.Branch)
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("merge: %v: %s", err, output)
		}
	}
	if vcs == "git" {
		// Hidden edits outside the selected cwd must keep acceptance open.
		for _, flag := range []string{"assume-unchanged", "skip-worktree"} {
			gitTestRun(t, childRoot, "update-index", "--"+flag, "file")
			writeTestFile(t, filepath.Join(childRoot, "file"), "hidden unfinished work")
			call("main", flag, true, accept)
			if data, err := os.ReadFile(filepath.Join(childRoot, "file")); err != nil || string(data) != "hidden unfinished work" {
				t.Fatal("acceptance changed hidden tracked edits", flag, err)
			}
			gitTestRun(t, childRoot, "update-index", "--no-"+flag, "file")
			gitTestRun(t, childRoot, "checkout", "--", "file")
		}
	}
	// Failed candidate batches do not accept the task, even after proof persistence.
	call("main", "rollback", true, accept, map[string]any{"op": "set", "p": "/missing", "state": "done"})
	nodes, err := p.journals.readTree(u.ctx, replay, workspace, "main", "", "", nil, "own")
	if err != nil || nodes[0].Children[0].State != "working" {
		t.Fatal("rejected batch accepted the result", nodes, err)
	}
	call("main", "accept", false, accept, map[string]any{"op": "set", "p": "/1", "state": "done"}, map[string]any{"op": "finish"})
	batches, err := p.orchestration.store.Snapshot(workspace, "main")
	if err != nil || batches[0].Integration == nil {
		t.Fatal("missing durable run proof", batches, err)
	}
	proof := *batches[0].Integration
	p.replayStore, err = openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	p.journals = newJournalStore()
	call("main", "accept", false, accept, map[string]any{"op": "set", "p": "/1", "state": "done"}, map[string]any{"op": "finish"})
	nodes, err = p.journals.readTree(u.ctx, p.replayStore, workspace, "main", "", "", nil, "own")
	accepted := nodes[0].Children[0]
	if err != nil || accepted.State != "accepted" || accepted.Finished == nil || accepted.Integration == nil || *accepted.Integration != proof {
		t.Fatal("acceptance lost after restart", accepted, err)
	}
	summary, err := summaryForTest(t, u.ctx, p.replayStore, workspace, "main")
	if err != nil || strings.Contains(summary.Text, "integration remains open") || !strings.Contains(summary.Text, "Resume: no runnable local task") {
		t.Fatal("accepted work remained open in recovery", summary, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "file"))
	if err != nil || string(content) != "unstaged source edit" {
		t.Fatal("source edits changed", string(content), err)
	}
	if vcs == "git" {
		command := exec.CommandContext(t.Context(), "git", "-C", workspace, "show", ":./file")
		if content, err := command.Output(); err != nil || string(content) != "staged source edit" {
			t.Fatal("source index changed", string(content), err)
		}
	}
	// Reopening starts a new review cycle; acceptance cannot use old task evidence.
	call("main", "reopen", false, map[string]any{"op": "set", "p": "/1", "state": "working"}, map[string]any{"op": "set", "p": "/1/1", "state": "working"})
	nodes, err = p.journals.readTree(u.ctx, p.replayStore, workspace, "main", "", "", nil, "own")
	if err != nil || nodes[0].Children[0].Integration != nil {
		t.Fatal("reopen retained current acceptance", nodes, err)
	}
	// Event replay keeps the accepted proof even after reopening clears the node.
	j, _, err := readThreadJournal(p.replayStore, workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	var event journalEvent
	for _, e := range j.Events {
		if e.Fields.State == "accepted" {
			event = e
		}
	}
	restored := threadJournal{}
	applyJournalReplayEvent(&restored, event)
	if len(restored.Items) != 1 || restored.Items[0].Integration == nil || *restored.Items[0].Integration != proof {
		t.Fatal("event replay lost integration proof", restored)
	}
}
