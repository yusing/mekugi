package router

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateShadowIntegrateRecovery(t *testing.T) {
	for _, state := range []string{"idle", "busy", "permissions"} {
		t.Run(state, func(t *testing.T) {
			workspace := t.TempDir()
			base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"
			file := filepath.Join(workspace, "file")
			writeTestFile(t, file, base)
			var old *appServerUI
			if state == "idle" {
				replay, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				var launch btwTestRPC
				old, launch = orchestratePendingWorkflow(t, replay, workspace, nil, &orchestrateSpawnInput{TaskName: "batch", Message: "bounded implementation"})
				orchestrateTestReply(t, old, launch, `{"turn":{"id":"implement"}}`)
			} else {
				old = orchestrateCleanupUIInWorkspace(t, workspace)
			}
			store, b := old.proxy.orchestration.store, old.orchestrateThreads["child"].batch
			gitTestRun(t, b.Cwd, "config", "user.name", "test")
			gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
			childText := strings.Replace(base, "one", "child", 1)
			writeTestFile(t, filepath.Join(b.Cwd, "file"), childText)
			gitTestCommit(t, b.Cwd)
			if state == "idle" {
				wire := old.client.Input.(*appServerTestInput)
				workflowActiveReviews(t, old, wire)
				workflowCompleted(t, old, "reviewer-s", "reviewer-s-turn", workflowVerdict(old.orchestrateThreads["child"], orchestrate.ReviewSimplification))
				workflowCompleted(t, old, "reviewer-c", "reviewer-c-turn", workflowVerdict(old.orchestrateThreads["child"], orchestrate.ReviewCorrectness))
				workflowNotice(t, old, wire)
				b = old.orchestrateThreads["child"].batch
				if b.Workflow.Phase != orchestrate.PhaseReady {
					t.Fatal("shadow implementation did not reach readiness", b.Workflow)
				}
			}
			sourceText := strings.Replace(base, "nine", "source", 1)
			writeTestFile(t, file, sourceText)
			u, w := resumeOrchestrationMain(t, store, workspace, old.proxy.replayStore)
			u.proxy.orchestration.store.ShadowSnapshot = orchestrateShadowSnapshot
			drainOrchestrateWork(t, u)
			w.Reset()
			retainOrchestratedTestPermissions(t, u, b)
			u.draft = "Main draft"
			c := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", target: "/root/batch", tool: "integrate", reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			select {
			case r := <-c.reply:
				t.Fatal("retained integration finished before resume admission", r.err)
			default:
			}
			read := btwTestRequest(t, w, "thread/read", "child")
			thread := map[string]any{"id": "child", "cwd": b.Cwd}
			btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
			var resume struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(w.Bytes(), &resume); err != nil || resume.Method != "thread/resume" {
				t.Fatal("expected host resume", w.String(), err)
			}
			w.Reset()
			if resume.Params["permissions"] != ":read-only" {
				t.Fatal("integration did not restore exact saved permissions", resume.Params)
			}
			select {
			case r := <-c.reply:
				t.Fatal("integration bypassed host admission", r.err)
			default:
			}
			if got, err := os.ReadFile(file); err != nil || string(got) != sourceText {
				t.Fatal("integration wrote source before admission", string(got), err)
			}
			status, profile := "completed", ":read-only"
			if state == "busy" {
				status = "inProgress"
			} else if state == "permissions" {
				profile = ":danger-full-access"
			}
			thread["turns"] = []any{map[string]any{"id": "current-turn", "status": status}}
			btwTestReply(t, u, btwTestRPC{ID: resume.ID}, string(mustMarshalJSON(map[string]any{
				"thread": thread, "model": "batch-model", "reasoningEffort": "high", "activePermissionProfile": map[string]any{"id": profile},
			})))
			if state != "permissions" {
				for _, method := range resumeTestSettle(t, u, w) {
					if method != "model/list" && method != "thread/list" {
						t.Fatal("integration dispatched unexpected host work", method)
					}
				}
			} else {
				unsubscribe := btwTestRequest(t, w, "thread/unsubscribe", "child")
				btwTestReply(t, u, unsubscribe, `{"status":"unsubscribed"}`)
			}
			drainOrchestrateWork(t, u)
			r := <-c.reply
			want := sourceText
			if state == "idle" {
				want = strings.Replace(sourceText, "one", "child", 1)
				if r.err != nil || r.batch.ShadowMerge == nil || r.batch.ShadowMerge.State != "applied" || r.batch.Integration == nil {
					t.Fatal("idle retained child did not integrate", r.batch, r.err)
				}
			} else if r.err == nil {
				t.Fatal("unadmitted or busy retained child integrated")
			}
			if state != "idle" {
				batches, err := store.Snapshot(workspace, "main")
				if err != nil || len(batches) != 1 || batches[0].ShadowMerge != nil {
					t.Fatal("rejected integration planned writeback", batches, err)
				}
			}
			if got, err := os.ReadFile(file); err != nil || string(got) != want {
				t.Fatal("integration lost source edits or rejected call wrote source", string(got), err)
			}
			if got, err := os.ReadFile(filepath.Join(b.Cwd, "file")); err != nil || string(got) != childText {
				t.Fatal("integration changed child files", string(got), err)
			}
			if u.viewedUI() != u || u.draft != "Main draft" || w.Len() != 0 {
				t.Fatal("integration switched views, changed draft or dispatched host work", w.String())
			}
		})
	}
}

func TestAppServerOrchestrateShadowIntegrateRejectsBeforeResume(t *testing.T) {
	for _, vcs := range []string{"git", "shadow"} {
		t.Run(vcs, func(t *testing.T) {
			workspace := orchestrateVCSWorkspace(t, vcs)
			var old *appServerUI
			if vcs == "git" {
				old = orchestrateCleanupUIInWorkspace(t, workspace)
			} else {
				replay, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				var launch btwTestRPC
				old, launch = orchestratePendingWorkflow(t, replay, workspace, nil, &orchestrateSpawnInput{TaskName: "batch", Message: "bounded implementation"})
				orchestrateTestReply(t, old, launch, `{"turn":{"id":"implement"}}`)
				store := old.proxy.orchestration.store
				if err := store.ObserveLaunch(t.Context(), workspace, "main", "batch", "completed", ""); err != nil {
					t.Fatal(err)
				}
				if err := store.RouteWorkflowCorrection(t.Context(), workspace, "main", "batch", "unclean implementer result", "reported completion"); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(workspace, "unfinished")
			writeTestFile(t, file, "source edits")
			u, w := resumeOrchestrationMain(t, old.proxy.orchestration.store, workspace, old.proxy.replayStore)
			drainOrchestrateWork(t, u)
			w.Reset()
			c := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", target: "batch", tool: "integrate", reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			select {
			case r := <-c.reply:
				if r.err == nil {
					t.Fatal("ineligible retained batch integrated")
				}
			default:
				t.Fatal("ineligible retained batch entered resume admission", w.String())
			}
			if w.Len() != 0 || len(u.navigation.resumes) != 0 || u.viewedUI() != u {
				t.Fatal("rejected integration dispatched host work or switched views", w.String())
			}
			if got, err := os.ReadFile(file); err != nil || string(got) != "source edits" {
				t.Fatal("rejected integration changed source edits", string(got), err)
			}
		})
	}
}
