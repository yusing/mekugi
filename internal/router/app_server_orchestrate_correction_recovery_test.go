package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/persistence"
)

func activeCorrectionUI(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, wire := workflowUI(t, "check")
	workflowCompleted(t, u, "child", "implement", "")
	workflowCheck(t, u, wire, "check", `{"exitCode":1}`)
	request, _ := workflowTake(t, u, wire, "turn/start", "child", orchestrate.DispatchCorrection)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"correction"}}`)
	child := u.orchestrateThreads["child"]
	if err := u.proxy.journals.beginJournalTurn(u.ctx, u.proxy.replayStore, child.batch.Cwd, "child", "correction"); err != nil {
		t.Fatal(err)
	}
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"correction"}}}`)
	u.modelsLoading, u.navigation.views["child"].modelsLoading = true, true
	if !u.switchOrchestratedThread("child") || !u.switchOrchestratedThread("main") || wire.Len() != 0 {
		t.Fatal("active correction navigation dispatched recovery", wire.String())
	}
	return u, wire
}

func completedCorrectionUI(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, wire := activeCorrectionUI(t)
	workflowCompleted(t, u, "child", "correction", "corrected")
	workflowCheck(t, u, wire, "check", `{"exitCode":0}`)
	workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	workflowNotice(t, u, wire)
	return u, wire
}

func TestOrchestrateCorrectionRecovery(t *testing.T) {
	for _, mode := range []string{"restart", "subscribed", "launch-saved", "journal-saved", "partial-save", "later-host-turn", "later-journal-turn", "later-live-turn", "later-save-turn", "wrong-thread", "missing-turn", "nonterminal"} {
		t.Run(mode, func(t *testing.T) {
			u, wire := completedCorrectionUI(t)
			child := u.orchestrateThreads["child"]
			cwd, store := child.batch.Cwd, u.proxy.orchestration.store
			if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate correction"), State: new("working"), Agent: "/root/batch"}}); err != nil {
				t.Fatal(err)
			}
			if mode != "journal-saved" {
				if err := u.proxy.journals.observeLifecycle(u.ctx, u.proxy.replayStore, cwd, "child", "working", ""); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "launch-saved" {
				if err := store.ObserveLaunch(t.Context(), u.session.cwd, "main", "batch", "running", ""); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "restart" {
				replay, err := openMekugiReplayStore(u.proxy.replayStore.directory)
				if err != nil {
					t.Fatal(err)
				}
				u, wire = resumeOrchestrationMain(t, store, u.session.cwd, replay)
				drainOrchestrateWork(t, u)
				// The shared resume fixture has already answered these lookups.
				workflowTake(t, u, wire, "model/list", "", "")
				workflowTake(t, u, wire, "thread/list", "", "")
				workflowTake(t, u, wire, "thread/list", "", "")
			} else if mode == "subscribed" {
				u.modelsLoading, u.navigation.views["child"].modelsLoading = true, true
				if !u.switchOrchestratedThread("child") || !u.switchOrchestratedThread("main") {
					t.Fatal("subscribed batch navigation failed")
				}
			} else {
				var err error
				u.navigation.retained, err = store.Snapshot(u.session.cwd, "main")
				if err != nil {
					t.Fatal(err)
				}
				u.resumeOrchestratedBatch("batch", func(err error) {
					if err != nil {
						t.Fatal(err)
					}
				})
			}
			request, _ := workflowTake(t, u, wire, "thread/read", "child", "")
			turns := []appServerHistoryTurn{{ID: "correction", Status: "completed"}}
			thread := appServerThreadInfo{ID: "child", Cwd: cwd, Turns: turns}
			switch mode {
			case "later-host-turn":
				thread.Turns = append(turns, appServerHistoryTurn{ID: "later", Status: "inProgress"})
			case "later-journal-turn":
				if err := u.proxy.journals.beginJournalTurn(u.ctx, u.proxy.replayStore, cwd, "child", "later"); err != nil {
					t.Fatal(err)
				}
			case "later-live-turn":
				orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"later"}}}`)
			case "wrong-thread":
				thread.ID = "unrelated"
			case "missing-turn":
				thread.Turns = nil
			case "nonterminal":
				thread.Turns[0].Status = "inProgress"
			}
			response := string(mustMarshalJSON(map[string]any{"thread": thread}))
			if mode == "launch-saved" {
				store.Writes = new(persistence.Counter)
			}
			if mode == "partial-save" {
				directory := store.Directory
				store.Directory = "invalid-relative-storage"
				orchestrateTestReply(t, u, request, response)
				store.Directory = directory
				j, _, err := readThreadJournal(u.proxy.replayStore, cwd, "child")
				batches, readErr := store.Snapshot(u.session.cwd, "main")
				if err != nil || readErr != nil || j.LifecycleState != "done" || batches[0].Launch.HostStatus != "running" || !strings.Contains(u.notice, "Correction lifecycle:") {
					t.Fatal("partial save lost the saved journal or hid the launch failure", err, readErr, u.notice)
				}
				u.resumeOrchestratedBatch("batch", func(err error) {
					if err != nil {
						t.Fatal(err)
					}
				})
				request, _ = workflowTake(t, u, wire, "thread/read", "child", "")
			}
			if mode == "later-save-turn" {
				// Hold launch persistence while a newer live turn starts.
				release := make(chan struct{})
				u.orchestrateWork(func() func() { <-release; return func() {} })
				btwTestReply(t, u, request, response)
				appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"later"}}}`)
				close(release)
				drainOrchestrateWork(t, u)
			} else {
				orchestrateTestReply(t, u, request, response)
			}
			j, _, err := readThreadJournal(u.proxy.replayStore, cwd, "child")
			batches, readErr := store.Snapshot(u.session.cwd, "main")
			if mode == "launch-saved" && store.Writes.Snapshot().Bytes != 0 {
				t.Fatal("journal repair rewrote unchanged launch state")
			}
			want := "done"
			if strings.HasPrefix(mode, "later-") || mode == "wrong-thread" || mode == "missing-turn" || mode == "nonterminal" {
				want = "working"
			}
			if err != nil || readErr != nil || j.LifecycleState != want || (batches[0].Launch.HostStatus == "completed") != (want == "done") {
				t.Fatalf("recovery: journal=%s launch=%s errors=%v,%v", j.LifecycleState, batches[0].Launch.HostStatus, err, readErr)
			}
			// The consuming integration gate still checks mounted host state.
			_, err = u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}})
			if (err == nil) != (want == "done") {
				t.Fatalf("acceptance for %s: %v", want, err)
			}
			if wire.Len() != 0 {
				t.Fatal("recovery dispatched host effects", wire.String())
			}
			if mode == "restart" && u.navigation.views["child"] != nil {
				t.Fatal("discovery subscribed the child")
			}
		})
	}
}

func TestOrchestrateCorrectionUnsettledRecovery(t *testing.T) {
	for _, mode := range []string{"matching-turn", "later-journal-turn", "later-save-turn", "older-journal-turn", "failed-older-journal-turn", "failed-launch-saved"} {
		t.Run(mode, func(t *testing.T) {
			newer := mode == "later-journal-turn"
			older := strings.Contains(mode, "older-journal-turn")
			failed := strings.HasPrefix(mode, "failed-")
			u, wire := activeCorrectionUI(t)
			const hostError = "host transport disconnected during correction"
			if mode == "failed-launch-saved" {
				// The live launch observation reached disk before workflow completion.
				if err := u.proxy.orchestration.store.ObserveLaunch(t.Context(), u.session.cwd, "main", "batch", "failed", hostError); err != nil {
					t.Fatal(err)
				}
			}
			workflowRestart(t, u)
			child := u.orchestrateThreads["child"]
			if older {
				// The journal retained only the authenticated initial launch turn.
				if err := u.proxy.journals.beginJournalTurn(u.ctx, u.proxy.replayStore, child.batch.Cwd, "child", child.batch.Launch.TurnID); err != nil {
					t.Fatal(err)
				}
			}
			if newer {
				if err := u.proxy.journals.beginJournalTurn(u.ctx, u.proxy.replayStore, child.batch.Cwd, "child", "later"); err != nil {
					t.Fatal(err)
				}
			}
			request, _ := workflowTake(t, u, wire, "thread/read", "child", "")
			thread := appServerThreadInfo{ID: "child", Cwd: child.batch.Cwd, Turns: []appServerHistoryTurn{{ID: "correction", Status: "completed"}}}
			if failed {
				thread.Turns[0].Status = "failed"
				thread.Turns[0].Error = &struct {
					Message string `json:"message"`
				}{hostError}
			}
			response := string(mustMarshalJSON(map[string]any{"thread": thread}))
			if mode == "later-save-turn" {
				release := make(chan struct{})
				u.orchestrateWork(func() func() { <-release; return func() {} })
				btwTestReply(t, u, request, response)
				appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"later"}}}`)
				close(release)
				drainOrchestrateWork(t, u)
			} else {
				orchestrateTestReply(t, u, request, response)
			}
			j, _, err := readThreadJournal(u.proxy.replayStore, child.batch.Cwd, "child")
			batches, readErr := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main")
			if readErr != nil {
				t.Fatal(readErr)
			}
			batch := batches[0]
			state, status, phase, turn := "done", "completed", orchestrate.PhaseValidation, "correction"
			if newer {
				state, status, phase, turn = "working", "running", orchestrate.PhaseCorrection, "later"
			}
			if mode == "later-save-turn" {
				state, status, phase = "working", "running", orchestrate.PhaseCorrection
			}
			if failed {
				state, status, phase = "blocked", "failed", orchestrate.PhaseFailed
				if batch.Error != hostError {
					t.Fatalf("recovery lost the retained host failure: %q", batch.Error)
				}
			}
			if older {
				state, turn = "working", "implement"
			}
			if err != nil || j.TurnID != turn || j.LifecycleState != state || batch.Launch.HostStatus != status || batch.Workflow.Phase != phase {
				t.Fatalf("unsettled recovery: turn=%s journal=%s launch=%s phase=%s error=%v", j.TurnID, j.LifecycleState, batch.Launch.HostStatus, batch.Workflow.Phase, err)
			}
			if wire.Len() != 0 {
				t.Fatal("discovery dispatched workflow effects", wire.String())
			}
		})
	}
}

func TestOrchestrateImplementationRecoveryHostError(t *testing.T) {
	u, wire := workflowUI(t)
	workflowRestart(t, u)
	child := u.orchestrateThreads["child"]
	request, _ := workflowTake(t, u, wire, "thread/read", "child", "")
	thread := map[string]any{"id": "child", "cwd": child.batch.Cwd, "turns": []any{map[string]any{
		"id": "implement", "status": "failed", "error": map[string]any{"message": "host failure"},
	}}}
	orchestrateTestReply(t, u, request, string(mustMarshalJSON(map[string]any{"thread": thread})))
	if child.batch.Error != "host failure" || child.batch.Launch.HostStatus != "failed" || child.batch.Workflow.Phase != orchestrate.PhaseFailed || wire.Len() != 0 {
		t.Fatalf("implementation recovery lost the terminal host result: %+v, requests=%s", child.batch, wire.String())
	}
}

func TestOrchestrateCorrectionLiveLifecycle(t *testing.T) {
	u, _ := completedCorrectionUI(t)
	child := u.orchestrateThreads["child"]
	j, exists, err := readThreadJournal(u.proxy.replayStore, child.batch.Cwd, "child")
	if err != nil || !exists || j.TurnID != "correction" || j.LifecycleState != "done" {
		t.Fatalf("live correction journal: turn=%s state=%s exists=%v err=%v", j.TurnID, j.LifecycleState, exists, err)
	}
	batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main")
	if err != nil || batches[0].Launch.HostStatus != "completed" {
		t.Fatalf("live correction launch: %+v, %v", batches, err)
	}
}
