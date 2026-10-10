package router

import (
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func orchestrateIdentityPendingTurn(t *testing.T) (*appServerUI, btwTestRPC) {
	return orchestrateIdentityPendingTurnWithReplay(t, nil)
}

func orchestrateIdentityPendingTurnWithReplay(t *testing.T, replay *mekugiReplayStore) (*appServerUI, btwTestRPC) {
	t.Helper()
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "file"), "base")
	gitTestCommit(t, workspace)
	return orchestrateIdentityPendingTurnInWorkspace(t, replay, workspace)
}

func orchestrateIdentityPendingTurnInWorkspace(t *testing.T, replay *mekugiReplayStore, workspace string) (*appServerUI, btwTestRPC) {
	return orchestrateIdentityPendingTurnWithEvidence(t, replay, workspace, nil)
}

func orchestrateIdentityPendingTurnWithEvidence(t *testing.T, replay *mekugiReplayStore, workspace string, inputs []orchestrate.EvidenceInput) (*appServerUI, btwTestRPC) {
	t.Helper()
	store := &orchestrate.Store{Directory: t.TempDir(), ShadowSnapshot: orchestrateShadowSnapshot}
	batch, err := store.Prepare(t.Context(), workspace, "main", "batch")
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 0 {
		batch, err = store.RetainEvidence(t.Context(), workspace, "main", "batch", inputs)
		if err != nil {
			t.Fatal(err)
		}
	}
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.ensureShell()
	t.Cleanup(func() { u.closeOrchestratedViews(); u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", workspace)
	u.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: replay, orchestration: &orchestrateRuntime{store: store}}
	if replay != nil {
		ctx, release, err := replay.beginSession(u.ctx, "main", "")
		if err != nil {
			t.Fatal(err)
		}
		u.ctx = ctx
		t.Cleanup(release)
	}
	if err := u.proxy.journals.initialize(u.ctx, replay, workspace, "main", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := u.proxy.journals.bindIdentity(u.ctx, replay, workspace, "main", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	u.model, u.reasoningEffort = "model", "high"
	u.statusConfig.Provider, u.statusConfig.ApprovalsReviewer = "mekugi_wrap", "user"
	u.statusConfig.Approval = []byte(`"never"`)
	u.statusConfig.PermissionProfile = []byte(`{"id":":workspace"}`)
	u.statusConfig.WorkspaceRoots = []string{workspace}
	command := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", input: orchestrateSpawnInput{TaskName: "batch", Message: "work"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(command)
	drainOrchestrateWork(t, u)
	request := btwTestRequest(t, w, "thread/start", "")
	response, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "child", "cwd": batch.Cwd}})
	orchestrateTestReply(t, u, request, string(response))
	return u, btwTestRequest(t, w, "turn/start", "child")
}
func TestAppServerOrchestrateDescendantJournal(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
	workspace := u.orchestrateThreads["child"].batch.Cwd
	for _, err := range []error{
		u.proxy.journals.initialize(t.Context(), nil, workspace, "native", "/root/worker", ""),
		u.proxy.journals.bindIdentity(t.Context(), nil, workspace, "native", "child", "/root/worker", true),
		u.proxy.journals.observeLifecycle(t.Context(), nil, workspace, "native", "working", ""),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	u.session.registerThread(appServerThreadInfo{ID: "native", Cwd: workspace, ParentThreadID: "child", AgentNickname: "worker"})
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"native","turn":{"id":"native-turn","status":"interrupted"}}}`)
	got := u.proxy.journals.memory[journalKey(workspace, "native")]
	if got.LifecycleState != "blocked" {
		t.Fatalf("native descendant journal remains %q after host interruption", got.LifecycleState)
	}
}
func TestAppServerOrchestrateDescendantRoster(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
	source := []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker","agent_role":"worker"}}}`)
	u.session.registerThread(appServerThreadInfo{ID: "main-native", ParentThreadID: "main", Source: source})
	u.registerSessionThread(appServerThreadInfo{ID: "child-native", Source: []byte(`{"subAgent":{"thread_spawn":{"parent_thread_id":"late-parent","agent_path":"/root/worker","agent_role":"worker"}}}`)})
	if u.session.paths["child-native"] == u.session.paths["main-native"] {
		t.Fatal("unknown ancestry borrowed Main identity")
	}
	u.registerSessionThread(appServerThreadInfo{ID: "nickname-child", ParentThreadID: "late-parent", AgentNickname: "nickname"})
	u.registerSessionThread(appServerThreadInfo{ID: "late-parent", ParentThreadID: "child", AgentNickname: "parent"})
	if u.session.paths["child-native"] != "/orchestrate/child/worker" || u.session.paths["nickname-child"] != "/orchestrate/child/nickname" {
		t.Fatal("late ancestry did not reconcile descendant", u.session.paths)
	}
	for _, thread := range []string{"main-native", "child-native"} {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"turn"}}}`, thread))
	}
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"running-turn","status":"completed"}}}`)
	if !u.orchestrateBusy() {
		t.Fatal("completed batch abandoned live native descendant")
	}
	orchestrateTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"child","item":{"type":"subAgentActivity","id":"activity","agentThreadId":"child-native","agentPath":"/root/worker"}}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child-native","turn":{"id":"turn","status":"completed"}}}`)
	if u.orchestrateBusy() {
		t.Fatal("completed descendants still block departure")
	}
	if !u.session.agent(u.session.paths["main-native"]).Responding {
		t.Fatal("child's native worker completion stopped Main's still-running native worker in roster")
	}
}

func TestAppServerOrchestrateRetainedDescendantWorkspace(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := replay.beginSession(t.Context(), "native", "native-session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	u.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: replay}
	workspace := t.TempDir()
	for _, scope := range []string{workspace, ""} {
		if err := u.proxy.journals.initialize(ctx, replay, scope, "native", "/root/worker", ""); err != nil {
			t.Fatal(err)
		}
		if err := u.proxy.journals.bindIdentity(ctx, replay, scope, "native", "child", "/root/worker", true); err != nil {
			t.Fatal(err)
		}
		orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"native","turn":{"id":"turn","status":"interrupted"}}}`)
		j, _, err := readThreadJournal(replay, scope, "native")
		if err != nil {
			t.Fatal(err)
		}
		if scope != "" && j.LifecycleState != "blocked" {
			t.Fatal("retained workspace did not receive pre-metadata lifecycle", j.LifecycleState)
		}
		if scope == "" && j.LifecycleState != "" {
			t.Fatal("ambiguous ownership mutated an unscoped journal", j.LifecycleState)
		}
	}
}
