package router

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func orchestrateCleanupUI(t *testing.T) *appServerUI {
	t.Helper()
	return orchestrateCleanupUIInWorkspace(t, orchestrateVCSWorkspace(t, "git"))
}

func orchestrateCleanupUIInWorkspace(t *testing.T, workspace string) *appServerUI {
	t.Helper()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, request := orchestrateIdentityPendingTurnInWorkspace(t, replay, workspace)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"initial"}}`)
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate batch"), Agent: "/root/batch", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"initial","status":"completed"}}}`)
	return u
}

func orchestrateCleanupMCPClient(t *testing.T, u *appServerUI) func(string, bool) *mcp.CallToolResult {
	t.Helper()
	p, store := u.proxy, u.proxy.orchestration.store
	serverWire, clientWire := mcp.NewInMemoryTransports()
	server, err := newOrchestrateMCPServer(p, store).Connect(t.Context(), serverWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	p.orchestration.active.Store(true)
	return func(thread string, wantError bool) *mcp.CallToolResult {
		t.Helper()
		result := make(chan *mcp.CallToolResult, 1)
		go func() {
			value, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "cleanup", Arguments: map[string]any{"target": "batch"}, Meta: mcp.Meta{"threadId": thread, "sessionId": "session", "callId": "cleanup", codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "turn"}}})
			if err != nil {
				t.Error(err)
			}
			result <- value
		}()
		select {
		case command := <-u.orchestrateCommands():
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
		case value := <-result:
			if !wantError || value == nil || !value.IsError {
				t.Fatal("caller admission", value)
			}
			return value
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup command timeout")
		}
		value := <-result
		if value == nil || value.IsError != wantError {
			t.Fatal("cleanup result", value)
		}
		return value
	}
}

func TestAppServerOrchestrateCleanupMCP(t *testing.T) {
	u := orchestrateCleanupUI(t)
	p, workspace := u.proxy, u.session.cwd
	store, child := p.orchestration.store, u.orchestrateThreads["child"]
	gitRead := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	batch := child.batch
	call := orchestrateCleanupMCPClient(t, u)
	call("main", true) // A run proof alone does not accept Main's task.
	if _, err := store.RecordIntegration(t.Context(), workspace, "main", "batch", "child"); err != nil {
		t.Fatal(err)
	}
	call("main", true)
	if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	call("child", true)
	v := u.navigation.views["child"]
	v.draft = "unfinished input"
	call("main", true)
	v.draft = ""
	writeTestFile(t, filepath.Join(batch.Cwd, "unfinished"), "work")
	call("main", true)
	if err := os.Remove(filepath.Join(batch.Cwd, "unfinished")); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, batch.Cwd, "checkout", "-qb", "other")
	call("main", true)
	gitTestRun(t, batch.Cwd, "checkout", "-q", batch.Branch)
	// Native host activity still gates a previously accepted batch.
	u.registerSessionThread(appServerThreadInfo{ID: "native", ParentThreadID: "child", Cwd: batch.Cwd, AgentNickname: "worker"})
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"native","turn":{"id":"native-turn"}}}`)
	call("main", true)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"native","turn":{"id":"native-turn","status":"completed"}}}`)
	input := orchestrate.Delivery{ID: "queued", From: "main", Target: "child", Message: "next", Deferred: true}
	if _, _, err := store.BeginDelivery(t.Context(), workspace, "main", input); err != nil {
		t.Fatal(err)
	}
	call("main", true)
	// Model the queue's ordinary host-confirmed consumption before cleanup.
	if _, _, _, err := store.BeginTurnDelivery(t.Context(), workspace, "main", orchestrate.Delivery{ID: "consume", From: "main", Target: "child", Message: "consume"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordDelivery(t.Context(), workspace, "main", "consume", "delivered", "settled-turn", ""); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "file"), "source index")
	gitTestRun(t, workspace, "add", "file")
	writeTestFile(t, filepath.Join(workspace, "file"), "source worktree")
	before := gitRead("diff", "HEAD") + gitRead("diff", "--cached")
	p.journals = newJournalStore() // Durable acceptance survives a fresh owner.
	call("main", false)
	call("main", false)
	if _, err := os.Lstat(batch.Checkout); !os.IsNotExist(err) {
		t.Fatal("accepted checkout survived", err)
	}
	if got := gitRead("rev-parse", "refs/heads/"+batch.Branch); got != batch.Base {
		t.Fatal("accepted branch was changed", got)
	}
	if got := gitRead("diff", "HEAD") + gitRead("diff", "--cached"); got != before {
		t.Fatal("cleanup changed source edits or index")
	}
	if err := v.send([]composerDraft{{text: "new work"}}, false); err != nil || v.draft != "new work" || !strings.Contains(v.notice, "cleanup") {
		t.Fatal("cleaned thread accepted input or lost its draft", err, v.draft)
	}
	w := u.client.Input.(*appServerTestInput)
	beforeWire := w.Len()
	for _, draft := range []string{"!printf run", "/btw question", "/compact"} {
		v.draft = draft
		if _, err := v.key('\r'); err != nil || v.draft != draft || w.Len() != beforeWire {
			t.Fatal("cleaned composer submitted an effect or lost input", draft, err, v.draft)
		}
	}
	retained, err := (&orchestrate.Store{Directory: store.Directory}).Snapshot(workspace, "main")
	if err != nil || retained[0].State != "removed" || retained[0].Integration == nil {
		t.Fatal("cleanup lost retained identity", retained, err)
	}
}

func TestAppServerOrchestrateCleanupReconciliation(t *testing.T) {
	u := orchestrateCleanupUI(t)
	p, workspace := u.proxy, u.session.cwd
	if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	store := p.orchestration.store
	batch := u.orchestrateThreads["child"].batch
	// Save uncertain intent, then simulate a lost native removal response.
	paths, err := filepath.Glob(filepath.Join(store.Directory, "*", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"state":"launched"`), []byte(`"state":"removing"`), 1)
	if err := os.WriteFile(paths[0], data, 0600); err != nil {
		t.Fatal(err)
	}
	proof, err := p.orchestrateCleanupProof(t.Context(), workspace, "main", "batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened := &orchestrate.Store{Directory: store.Directory}
	if _, err := reopened.Cleanup(t.Context(), workspace, "main", "batch", proof, nil); err == nil {
		t.Fatal("repeated uncertain removal of a surviving checkout")
	}
	missing := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(batch.Checkout, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Cleanup(t.Context(), workspace, "main", "batch", proof, nil); err == nil {
		t.Fatal("accepted a missing checkout with a live Git registration")
	}
	if err := os.Rename(missing, batch.Checkout); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, workspace, "worktree", "remove", "--", batch.Checkout)
	got, err := reopened.Cleanup(t.Context(), workspace, "main", "batch", proof, nil)
	if err != nil || got.State != "removed" {
		t.Fatal("failed to reconcile confirmed removal", got, err)
	}
}

func TestUISnapshotOrchestrateCleanedRoster(t *testing.T) {
	u := orchestrateCleanupUI(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	u.agents.clock = u.clock
	u.status = "Ready"
	u.navigation.views["child"].status = "Completed"
	child := u.orchestrateThreads["child"]
	child.batch.State, child.batch.Branch = "removed", "mekugi/run/batch"
	u.orchestrationRoster()
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-cleaned-roster.txt", strings.Join(u.agents.nativeRoster(100, 12, now, true), "\n")+"\n")
}

func TestAppServerOrchestrateCleanupAcceptanceInterleaving(t *testing.T) {
	u := orchestrateCleanupUI(t)
	p, workspace := u.proxy, u.session.cwd
	store := p.orchestration.store
	setState := func(state string) error {
		_, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new(state)}})
		return err
	}
	if err := setState("accepted"); err != nil {
		t.Fatal(err)
	}
	proof, err := p.orchestrateCleanupProof(t.Context(), workspace, "main", "batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A mutation after preflight invalidates removal before intent is saved.
	if err := setState("working"); err != nil {
		t.Fatal(err)
	}
	authorize := func(publish func() error) error {
		_, err := p.orchestrateCleanupProof(t.Context(), workspace, "main", "batch", publish)
		return err
	}
	if _, err := store.Cleanup(t.Context(), workspace, "main", "batch", proof, authorize); err == nil {
		t.Fatal("stale acceptance authorized removal")
	}
	batch := u.orchestrateThreads["child"].batch
	if _, err := os.Stat(batch.Checkout); err != nil {
		t.Fatal("stale acceptance removed the checkout", err)
	}
	if err := setState("accepted"); err != nil {
		t.Fatal(err)
	}
	// Once publication wins, a fresh journal owner cannot reopen the binding
	// between journal authorization and the Git effect. Other edits still work.
	got, err := store.Cleanup(t.Context(), workspace, "main", "batch", proof, func(publish func() error) error {
		if err := authorize(publish); err != nil {
			return err
		}
		p.journals = newJournalStore()
		if err := setState("working"); err == nil {
			t.Fatal("reopened acceptance during removal")
		}
		if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", Title: new("Reviewed batch")}}); err != nil {
			t.Fatal("cleanup prevented an unrelated journal edit", err)
		}
		return nil
	})
	if err != nil || got.State != "removed" {
		t.Fatal("authorized cleanup failed", got, err)
	}
}
