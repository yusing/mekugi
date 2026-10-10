package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func orchestrateTargetMCPClient(t *testing.T, u *appServerUI, tool string) func(string, bool) *mcp.CallToolResult {
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
			value, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"target": "batch"}, Meta: mcp.Meta{"threadId": thread, "sessionId": "session", "callId": tool, codexTurnMetadataHeader: map[string]any{"thread_id": thread, "turn_id": "turn"}}})
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
			t.Fatal(tool, "command timeout")
		}
		value := <-result
		if value == nil || value.IsError != wantError {
			t.Fatal(tool, "result", value)
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
	call := orchestrateTargetMCPClient(t, u, "cleanup")
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
	if err := v.send([]composerDraft{{text: "new work"}}, false); err != nil || v.draft != "new work" || !strings.Contains(v.notice, "unavailable") {
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

func TestAppServerOrchestrateCleanupEvidenceMCP(t *testing.T) {
	workspace, source := orchestrateVCSWorkspace(t, "git"), filepath.Join(t.TempDir(), "source")
	writeTestFile(t, source, "original")
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, request := orchestrateIdentityPendingTurnWithEvidence(t, replay, workspace, []orchestrate.EvidenceInput{{Name: "ready", Source: source}, {Name: "changed", Source: source}, {Name: "replaced", Source: source}})
	orchestrateTestReply(t, u, request, `{"turn":{"id":"initial"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"initial","status":"completed"}}}`)
	b := u.orchestrateThreads["child"].batch
	call := orchestrateTargetMCPClient(t, u, "cleanup")
	call("main", true) // Evidence cannot be removed before journal acceptance.
	if _, err := os.Stat(b.Evidence[0].Path); err != nil {
		t.Fatal("removed unaccepted evidence", err)
	}
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate batch"), Agent: "/root/batch", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, b.Evidence[1].Path, "changed")
	if err := os.Rename(b.Evidence[2].Path, b.Evidence[2].Path+".unknown"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, b.Evidence[2].Path, "original")
	unknown := filepath.Join(filepath.Dir(b.Evidence[0].Path), "unknown")
	writeTestFile(t, unknown, "unknown")
	result := call("main", true) // Partial failure still confirms checkout removal.
	data, err := json.Marshal(result.StructuredContent)
	var partial orchestrate.Batch
	if err != nil || json.Unmarshal(data, &partial) != nil || partial.State != "removed" || len(partial.Evidence) != 3 || partial.Evidence[0].State != "removed" || partial.Evidence[1].Error == "" || partial.Evidence[2].Error == "" {
		t.Fatal("MCP lost partial cleanup outcomes", result, err)
	}
	if _, err := os.Lstat(b.Checkout); !os.IsNotExist(err) {
		t.Fatal("partial evidence cleanup retained accepted checkout", err)
	}
	if _, err := os.Lstat(b.Evidence[0].Path); !os.IsNotExist(err) {
		t.Fatal("unchanged evidence survived", err)
	}
	store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	u.proxy.orchestration.store, u.proxy.journals = store, newJournalStore()
	call("main", true) // A fresh storage owner preserves the partial result.
	retained, err := store.Snapshot(workspace, "main")
	if err != nil || retained[0].State != "removed" || retained[0].Evidence[0].State != "removed" || retained[0].Evidence[1].Error == "" {
		t.Fatal("lost durable partial outcome", retained, err)
	}
	for path, want := range map[string]string{source: "original", b.Evidence[1].Path: "changed", unknown: "unknown", b.Evidence[2].Path: "original", b.Evidence[2].Path + ".unknown": "original"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatal("changed preserved evidence", path, string(got), err)
		}
	}
}

func TestAppServerOrchestrateEvidenceCleanupAcceptance(t *testing.T) {
	workspace, source := orchestrateVCSWorkspace(t, "git"), filepath.Join(t.TempDir(), "source")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse large copy keeps the real hashing interval observable after Git
	// removes the checkout, without adding a production synchronization hook.
	if err := file.Truncate(128 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, request := orchestrateIdentityPendingTurnWithEvidence(t, replay, workspace, []orchestrate.EvidenceInput{{Name: "copy", Source: source}})
	orchestrateTestReply(t, u, request, `{"turn":{"id":"initial"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"initial","status":"completed"}}}`)
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate batch"), Agent: "/root/batch", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	store := u.proxy.orchestration.store
	proof, err := u.proxy.orchestrateCleanupProof(t.Context(), workspace, "main", "batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Cleanup(t.Context(), workspace, "main", "batch", proof, func(publish func() error) error {
			_, err := u.proxy.orchestrateCleanupProof(t.Context(), workspace, "main", "batch", publish)
			return err
		})
		done <- err
	}()
	b := u.orchestrateThreads["child"].batch
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Lstat(b.Checkout); os.IsNotExist(err) {
			retained, err := store.Snapshot(workspace, "main")
			if err != nil {
				t.Fatal(err)
			}
			if retained[0].State == "removed" || retained[0].Evidence[0].State == "removing" {
				if retained[0].State != "removing" {
					<-done
					t.Fatal("acceptance reservation ended before evidence removal")
				}
				break
			}
		}
		select {
		case err := <-done:
			t.Fatal("missed evidence cleanup interval", err)
		case <-deadline.C:
			t.Fatal("checkout removal timeout")
		case <-time.After(time.Millisecond):
		}
	}
	_, reopenErr := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("working")}})
	cleanupErr := <-done
	if reopenErr == nil || cleanupErr != nil {
		t.Fatal("acceptance changed during evidence removal", reopenErr, cleanupErr)
	}
}

func TestAppServerOrchestrateCleanupReconciliation(t *testing.T) {
	for _, kind := range []string{"git", "shadow"} {
		t.Run(kind, func(t *testing.T) {
			workspace := orchestrateVCSWorkspace(t, kind)
			u := orchestrateCleanupUIInWorkspace(t, workspace)
			if kind != "git" {
				orchestrateTargetMCPClient(t, u, "integrate")("main", false)
			}
			p := u.proxy
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
			repository := workspace
			if kind != "git" {
				repository = batch.Repository
			}
			gitTestRun(t, repository, "worktree", "remove", "--", batch.Checkout)
			got, err := reopened.Cleanup(t.Context(), workspace, "main", "batch", proof, nil)
			if err != nil || got.State != "removed" {
				t.Fatal("failed to reconcile confirmed removal", got, err)
			}
		})
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

func TestAppServerOrchestrateShadowCleanupMCP(t *testing.T) {
	for _, kind := range []string{"shadow", "svn"} {
		t.Run(kind, func(t *testing.T) {
			workspace := orchestrateVCSWorkspace(t, kind)
			u := orchestrateCleanupUIInWorkspace(t, workspace)
			b := u.orchestrateThreads["child"].batch
			writeTestFile(t, filepath.Join(b.Cwd, "file"), "child")
			gitTestRun(t, b.Cwd, "config", "user.name", "test")
			gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
			gitTestCommit(t, b.Cwd)
			call := orchestrateTargetMCPClient(t, u, "cleanup")
			call("main", true)
			orchestrateTargetMCPClient(t, u, "integrate")("main", false)
			if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(b.Cwd, "file"), "unfinished")
			gitTestRun(t, b.Cwd, "update-index", "--assume-unchanged", "file")
			call("main", true)
			gitTestRun(t, b.Cwd, "update-index", "--no-assume-unchanged", "file")
			writeTestFile(t, filepath.Join(b.Cwd, "file"), "child")
			var metadata map[string][32]byte
			if kind == "svn" {
				metadata = svnMetadata(t, workspace)
			}
			// Cleanup consumes retained acceptance, preserving later source edits.
			writeTestFile(t, filepath.Join(workspace, "file"), "later source")
			store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
			u.proxy.orchestration.store, u.proxy.journals = store, newJournalStore()
			call("main", false)
			call("main", false)
			retained, err := store.Snapshot(workspace, "main")
			if err != nil || retained[0].State != "removed" {
				t.Fatal("lost cleanup outcome", retained, err)
			}
			if _, err := os.Lstat(b.Checkout); !os.IsNotExist(err) {
				t.Fatal("accepted shadow checkout survived", err)
			}
			gitTestRun(t, b.Repository, "cat-file", "-e", retained[0].Integration.Tip+"^{commit}")
			gitTestRun(t, b.Repository, "cat-file", "-e", retained[0].Integration.SourceTip+"^{tree}")
			gitTestRun(t, b.Repository, "show-ref", "--verify", "refs/heads/"+b.Branch)
			if data, err := os.ReadFile(filepath.Join(workspace, "file")); err != nil || string(data) != "later source" {
				t.Fatal("cleanup changed source", string(data), err)
			}
			if kind == "svn" && !reflect.DeepEqual(metadata, svnMetadata(t, workspace)) {
				t.Fatal("cleanup changed SVN metadata")
			}
		})
	}
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
