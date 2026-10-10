package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func resumeOrchestrationMain(t *testing.T, store *orchestrate.Store, workspace string, replay ...*mekugiReplayStore) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, wire := newAppServerTestUI()
	u.ctx, u.thread, u.resumeThread = t.Context(), "", "main"
	u.proxy = &mekugiProxy{journals: newJournalStore(), activity: newSubagentActivity(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: store.Directory}}}
	if len(replay) != 0 {
		u.proxy.replayStore = replay[0]
	}
	u.ensureShell()
	t.Cleanup(func() { u.closeOrchestratedViews(); u.shell.diff.close(); u.shell.diffScreen.Close() })
	if err := u.requestResume("main"); err != nil {
		t.Fatal(err)
	}
	read := btwTestRequest(t, wire, "thread/read", "main")
	thread := map[string]any{"id": "main", "cwd": workspace}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
	resume := btwTestRequest(t, wire, "thread/resume", "main")
	btwTestReply(t, u, resume, string(mustMarshalJSON(map[string]any{"thread": thread, "model": "model", "reasoningEffort": "high"})))
	// Resume's ordinary model lookup and two roster pages retain their existing owner.
	appServerTestMessage(t, u, `{"id":3,"result":{"data":[]}}`)
	appServerTestMessage(t, u, `{"id":4,"result":{"data":[]}}`)
	appServerTestMessage(t, u, `{"id":5,"result":{"data":[]}}`)
	return u, wire
}

func TestUISnapshotOrchestrateRecoveryResume(t *testing.T) {
	old, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, old, launch, `{"turn":{"id":"old-turn"}}`)
	orchestrateTestMessage(t, old, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"old-turn"}}}`)
	store, workspace := old.proxy.orchestration.store, old.session.cwd
	prepared, err := store.Prepare(t.Context(), workspace, "main", "prepared")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(prepared.Cwd, "unfinished"), "keep this work")
	branch := strings.TrimSuffix(prepared.Branch, "/prepared") + "/failed"
	gitTestRun(t, workspace, "branch", branch)
	if failed, err := store.Prepare(t.Context(), workspace, "main", "failed"); err == nil || failed.State != "failed" {
		t.Fatal("failure fixture did not retain intent", failed, err)
	}
	if _, err := store.Prepare(t.Context(), workspace, "other", "private"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(t.Context(), workspace, "main", "uncertain"); err != nil {
		t.Fatal(err)
	}
	if _, dispatch, err := store.BeginLaunch(t.Context(), workspace, "main", "uncertain", "retained assignment", []byte("{}")); err != nil || !dispatch {
		t.Fatal("uncertain launch fixture failed", err)
	}
	u, wire := resumeOrchestrationMain(t, store, workspace)
	drainOrchestrateWork(t, u)
	if u.restoring != nil || u.navigation == nil || len(u.navigation.views) != 1 || len(u.navigation.retained) != 4 || len(u.orchestrateThreads) != 0 || u.orchestrateBusy() {
		t.Fatal("resume revived lifecycle handles or lost the run")
	}
	if got, err := os.ReadFile(filepath.Join(prepared.Cwd, "unfinished")); err != nil || string(got) != "keep this work" {
		t.Fatal("unfinished checkout changed", err)
	}
	batches, err := store.Snapshot(workspace, "main")
	if err != nil || batches[0].Launch.HostStatus != "running" {
		t.Fatal("discovery changed retained observations", err, batches)
	}
	// A subscribed controller takes precedence over the same retained task.
	old.navigation.retained = u.navigation.retained
	old.orchestrationRoster()
	count := 0
	for _, agent := range old.agents.orchestration {
		if agent.Name == "/Orchestration/batch" {
			count++
		}
	}
	if count != 1 || strings.Contains(old.agents.orchestrationLabels["/Orchestration/batch"], "not subscribed") {
		t.Fatal("retained facts replaced a subscribed row")
	}
	wire.Reset()
	u.agents.selected = "/Orchestration/uncertain"
	u.shell.focus = 3
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || u.viewedUI() != u || !strings.Contains(u.notice, "no confirmed thread") {
		t.Fatal("retained selection dispatched a host effect")
	}
	u.shell.focus = 0
	appServerTestKeys(t, u, "/orchestrate\r")
	drainOrchestrateWork(t, u)
	if !strings.Contains(u.picker.choices[1].description, "interrupted") || wire.Len() != 0 {
		t.Fatal("picker disagrees with restored roster")
	}
	appServerTestKeys(t, u, "\x1b")
	orchestrateTestMessage(t, old, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"old-turn","status":"failed"}}}`)
	appServerTestKeys(t, u, "/orchestrate\r")
	drainOrchestrateWork(t, u)
	if !strings.Contains(u.picker.choices[1].description, "failed") {
		t.Fatal("restored facts replaced the picker's newer observation")
	}
	// Fix temporary branch hashes in the renderer's observation inputs.
	for i := range u.navigation.retained {
		b := &u.navigation.retained[i]
		b.Branch = "mekugi/run/" + b.TaskName
	}
	u.orchestrationRoster()
	u.agents.selected = "/Orchestration/batch"
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-restored-roster.txt", strings.Join(u.agents.nativeRoster(100, 12, u.now(), true), "\n")+"\n")
}

func TestAppServerOrchestrateRecoveryReadAdmission(t *testing.T) {
	for _, mode := range []string{"empty", "failure", "changed-main", "changed-workspace"} {
		t.Run(mode, func(t *testing.T) {
			store := &orchestrate.Store{Directory: t.TempDir()}
			if mode == "failure" {
				store.Directory = "relative"
			}
			workspace := t.TempDir()
			if strings.HasPrefix(mode, "changed-") {
				workspace = gitTestWorkspace(t)
				writeTestFile(t, filepath.Join(workspace, "file"), "base")
				gitTestCommit(t, workspace)
				if _, err := store.Prepare(t.Context(), workspace, "main", "retained"); err != nil {
					t.Fatal(err)
				}
			}
			u, wire := resumeOrchestrationMain(t, store, workspace)
			if mode == "changed-main" {
				u.thread = "other"
				u.session.start("other", workspace)
			} else if mode == "changed-workspace" {
				u.session.start("main", t.TempDir())
			}
			drainOrchestrateWork(t, u)
			if u.navigation != nil {
				t.Fatal("read admitted an absent or different run")
			}
			if mode == "failure" && !strings.Contains(u.notice, "Orchestration restore:") {
				t.Fatal("read failure became empty success")
			}
			if strings.Contains(wire.String(), "turn/start") || strings.Contains(wire.String(), "thread/start") {
				t.Fatal("read recreated execution")
			}
			if mode == "empty" {
				entries, err := os.ReadDir(store.Directory)
				if err != nil || len(entries) != 0 {
					t.Fatal("discovery created run state", err)
				}
			}
		})
	}
}
