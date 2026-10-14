package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateCanceledLaunchRecovery(t *testing.T) {
	for _, mode := range []string{"live", "late-cancel", "restart", "uncertain", "uncertain-restart", "recovery-uncertain", "wrong-cwd", "host-turn", "live-host-turn", "inflight-host-turn", "missing-permissions", "damaged-evidence", "composer"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			workspace := gitTestWorkspace(t)
			writeTestFile(t, filepath.Join(workspace, "selected", "file"), "base")
			gitTestCommit(t, workspace)
			u, initial := orchestrateIdentityPendingTurnInWorkspace(t, nil, filepath.Join(workspace, "selected"))
			orchestrateTestReply(t, u, initial, `{"turn":{"id":"existing-turn"}}`)
			w := u.client.Input.(*appServerTestInput)
			store := u.proxy.orchestration.store
			batch, err := store.Prepare(t.Context(), u.session.cwd, "main", "recover")
			if err != nil {
				t.Fatal(err)
			}
			evidence := filepath.Join(t.TempDir(), "evidence")
			writeTestFile(t, evidence, "retained input")
			batch, err = store.RetainEvidence(t.Context(), u.session.cwd, "main", "recover", []orchestrate.EvidenceInput{{Name: "input", Source: evidence}})
			if err != nil {
				t.Fatal(err)
			}
			// The original custom launch policy must not replace newer saved permissions.
			u.statusConfig.PermissionProfile = []byte(`null`)
			u.statusConfig.Sandbox = appServerSandboxPolicy{Type: "workspaceWrite", WritableRoots: []string{u.session.cwd}, ExcludeSlashTmp: true}
			if mode != "missing-permissions" {
				u.panes = new(nativePanePersistence)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			launch := &orchestrateCommand{ctx: ctx, workspace: u.session.cwd, main: "main", tool: "spawn_agent", input: orchestrateSpawnInput{TaskName: "recover", Message: "original assignment"}, reply: make(chan orchestrateResult, 1)}
			if mode == "live-host-turn" || mode == "inflight-host-turn" {
				launch.input.Kind = orchestrate.WorkflowDiagnosis
			}
			u.startOrchestratedChild(launch)
			drainOrchestrateWork(t, u)
			req := btwTestRequest(t, w, "thread/start", "")
			uncertain := mode == "uncertain" || mode == "uncertain-restart"
			if !uncertain && mode != "late-cancel" {
				cancel()
			}
			btwTestReply(t, u, req, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": "recover-child", "cwd": batch.Cwd}, "model": "model", "approvalPolicy": "never", "activePermissionProfile": nil, "sandbox": map[string]any{"type": "readOnly"}})))
			if mode == "late-cancel" {
				// Thread publication completes, then cancellation wins before view readiness.
				u.completeOrchestrateWork(<-u.orchestrateCompletions)
				cancel()
			}
			drainOrchestrateWork(t, u)
			if uncertain {
				_ = btwTestRequest(t, w, "turn/start", "recover-child")
				cancel() // No acknowledgement: the host may already have run it.
			} else if r := <-launch.reply; !errors.Is(r.err, context.Canceled) {
				t.Fatal("launch did not retain cancellation", r.err)
			}
			if w.Len() != 0 {
				t.Fatal("cancellation dispatched work", w.String())
			}
			// Repeating spawn must not override cancellation or repeat thread/start.
			repeat := *launch
			repeat.ctx, repeat.reply = t.Context(), make(chan orchestrateResult, 1)
			u.startOrchestratedChild(&repeat)
			drainOrchestrateWork(t, u)
			if r := <-repeat.reply; r.err != nil || r.batch.Launch.ThreadID != "recover-child" || w.Len() != 0 {
				t.Fatal("repeated spawn changed retained launch", r.err, w.String())
			}
			restart := mode == "restart" || mode == "wrong-cwd" || mode == "host-turn" || mode == "uncertain-restart" || mode == "missing-permissions"
			if restart {
				u, w = resumeOrchestrationMain(t, store, u.session.cwd)
				drainOrchestrateWork(t, u)
				if mode == "uncertain-restart" {
					read, _ := workflowTake(t, u, w, "thread/read", "recover-child", "")
					orchestrateTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": "recover-child", "cwd": batch.Cwd, "turns": []any{}}})))
				}
				if strings.Contains(w.String(), `"threadId":"recover-child"`) || strings.Contains(w.String(), `"turn/start"`) {
					t.Fatal("recovery dispatched work without input", w.String())
				}
				w.Reset() // Main's already-consumed model and roster requests.
				// The ordinary permissions owner retains host-confirmed settings.
				if mode != "missing-permissions" {
					previous, _ := appliedSettingsTestUI(t, batch.Cwd, "recover-child")
					previous.navigation = &orchestrateNavigation{owner: u}
					appServerTestNotify(t, previous, "thread/settings/updated", map[string]any{"threadId": "recover-child", "threadSettings": map[string]any{"model": "model", "effort": "high", "activePermissionProfile": map[string]any{"id": ":workspace"}}})
				}
				u.panes = new(nativePanePersistence)
			}
			if mode == "live" {
				appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "recover-child", "threadSettings": map[string]any{"activePermissionProfile": map[string]any{"id": ":read-only"}}})
			}
			if mode == "live-host-turn" {
				orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"recover-child","turn":{"id":"unexpected","status":"interrupted"}}}`)
			}
			if mode == "damaged-evidence" {
				writeTestFile(t, batch.Evidence[0].Path, "changed input")
			}
			if mode == "composer" {
				if !u.switchOrchestratedThread("recover-child") {
					t.Fatal("canceled child view is unavailable")
				}
				view := u.viewedUI()
				view.shell.focus = 0
				appServerTestKeys(t, view, "User requested resume\r")
				if strings.Contains(w.String(), `"turn/start"`) || !strings.Contains(view.notice, "coordinator follow-up") {
					t.Fatal("composer bypassed the retained first handoff", view.notice, w.String())
				}
				if w.Len() != 0 {
					orchestrateTestReply(t, u, btwTestRequest(t, w, "model/list", ""), `{"data":[]}`)
				}
			}
			follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "recover", tool: "followup_task", callID: "resume-canceled", input: orchestrateSpawnInput{Message: "User requested resume"}, reply: make(chan orchestrateResult, 1)}
			if mode == "live" || mode == "restart" || mode == "uncertain-restart" || mode == "recovery-uncertain" {
				follow.reviewScope = "Amended scope after canceled first input"
			}
			u.startOrchestratedChild(follow)
			if mode == "inflight-host-turn" {
				// Host completion revokes unsent proof while reservation is in progress.
				appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"recover-child","turn":{"id":"unexpected","status":"interrupted"}}}`)
			}
			drainOrchestrateWork(t, u)
			if mode == "damaged-evidence" {
				if w.Len() != 0 {
					t.Fatal("first-turn recovery dispatched despite changed retained evidence", w.String())
				}
				if r := <-follow.reply; r.err == nil {
					t.Fatal("changed retained evidence was accepted")
				}
				writeTestFile(t, batch.Evidence[0].Path, "retained input")
				u.startOrchestratedChild(follow)
				drainOrchestrateWork(t, u)
			}
			if mode == "uncertain" || mode == "live-host-turn" || mode == "inflight-host-turn" {
				if w.Len() != 0 {
					t.Fatal("host evidence did not prevent first-turn replay", w.String())
				}
				if r := <-follow.reply; r.err == nil || w.Len() != 0 {
					t.Fatal("uncertain first turn was retried", r.err, w.String())
				}
				return
			}
			select {
			case r := <-follow.reply:
				t.Fatalf("known-unsent launch could not resume: %v", r.err)
			default:
			}
			if restart {
				thread := map[string]any{"id": "recover-child", "cwd": batch.Cwd, "turns": []any{}}
				if mode == "wrong-cwd" {
					thread["cwd"] = batch.Checkout
				}
				orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/read", "recover-child"), string(mustMarshalJSON(map[string]any{"thread": thread})))
				if mode == "wrong-cwd" || mode == "missing-permissions" {
					if r := <-follow.reply; r.err == nil || w.Len() != 0 {
						t.Fatal("changed host evidence admitted recovery", r.err, w.String())
					}
					return
				}
				if mode == "host-turn" {
					thread["turns"] = []any{map[string]any{"id": "unexpected", "status": "completed"}}
				}
				if !strings.Contains(w.String(), `"permissions":":workspace"`) {
					t.Fatal("resume lost saved host permissions", w.String())
				}
				orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/resume", "recover-child"), string(mustMarshalJSON(map[string]any{"thread": thread, "activePermissionProfile": map[string]any{"id": ":workspace"}})))
				if mode == "host-turn" {
					if r := <-follow.reply; r.err == nil || w.Len() != 0 {
						t.Fatal("host execution evidence admitted recovery", r.err, w.String())
					}
					return
				}
				for range 2 {
					lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
					w.Reset()
					for _, line := range lines {
						var request btwTestRPC
						if err := json.Unmarshal(line, &request); err != nil || request.Method != "model/list" && request.Method != "thread/list" {
							t.Fatalf("unexpected resume request %s: %v", line, err)
						}
						orchestrateTestReply(t, u, request, `{"data":[]}`)
					}
				}
			}
			if mode == "uncertain-restart" {
				// Recovery reports uncertainty to Main without starting model work.
				if strings.Contains(w.String(), `"thread/inject_items"`) {
					orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
				}
				if r := <-follow.reply; r.err == nil || w.Len() != 0 {
					t.Fatal("restart retried an unacknowledged first turn", r.err, w.String())
				}
				return
			}
			if strings.Contains(w.String(), `"sandboxPolicy"`) {
				t.Fatal("follow-up replaced current host permissions with the launch policy", w.String())
			}
			req = btwTestRequest(t, w, "turn/start", "recover-child")
			text := req.Params.Input[0].Text
			if follow.reviewScope != "" && !strings.Contains(text, follow.reviewScope) {
				t.Fatal("known-unsent input lost scope amendment", text)
			}
			if !strings.Contains(text, "original assignment") || !strings.Contains(text, follow.input.Message) || !strings.Contains(text, batch.Evidence[0].Path) || strings.Count(text, "Your coordinator is main.") != 1 {
				t.Fatal("recovery lost the initial handoff", text)
			}
			if mode == "recovery-uncertain" {
				orchestrateTestReply(t, u, req, `{}`)
				if r := <-follow.reply; r.err == nil || r.delivery.State != "uncertain" {
					t.Fatal("missing recovery acknowledgement was not uncertain", r)
				}
				u.startOrchestratedChild(follow)
				drainOrchestrateWork(t, u)
				if r := <-follow.reply; r.err != nil || r.delivery.State != "uncertain" || w.Len() != 0 {
					t.Fatal("uncertain recovery was not retained", r.err, w.String())
				}
				follow.callID = "another-recovery"
				u.startOrchestratedChild(follow)
				drainOrchestrateWork(t, u)
				if r := <-follow.reply; r.err == nil || w.Len() != 0 {
					t.Fatal("uncertain recovery was dispatched again", r.err, w.String())
				}
				return
			}
			orchestrateTestReply(t, u, req, `{"turn":{"id":"recovered-turn"}}`)
			if r := <-follow.reply; r.err != nil || r.delivery == nil || r.delivery.State != "delivered" {
				t.Fatal("recovery acknowledgement failed", r.err)
			}
			retained, err := store.Snapshot(u.session.cwd, "main")
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range retained {
				if b.TaskName == "recover" && (b.Launch.ThreadID != "recover-child" || b.Launch.TurnID != "recovered-turn" || b.Cwd != batch.Cwd || b.Workflow.Phase != orchestrate.PhaseImplementation) {
					t.Fatal("recovery lost launch or workflow identity", fmt.Sprintf("%+v", b))
				}
			}
			u.startOrchestratedChild(follow)
			drainOrchestrateWork(t, u)
			if r := <-follow.reply; r.err != nil || w.Len() != 0 {
				t.Fatal("repeated recovery duplicated host work", r.err, w.String())
			}
		})
	}
}
