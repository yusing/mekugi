package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateResumeCustomSandbox(t *testing.T) {
	for _, rootCount := range []int{0, 50} {
		t.Run(fmt.Sprintf("roots=%d", rootCount), func(t *testing.T) {
			stateDir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", stateDir)
			u, w, b := retainedOrchestratedUI(t)
			// Save a post-launch policy through the ordinary host-settings consumer.
			previous, _ := appliedSettingsTestUI(t, b.Cwd, "child")
			previous.navigation = &orchestrateNavigation{owner: u}
			roots := make([]any, rootCount)
			for i := range roots {
				roots[i] = filepath.Join(b.Cwd, fmt.Sprintf("explicit-writable-root-%03d", i))
			}
			policy := map[string]any{"type": "workspaceWrite", "networkAccess": true,
				"writableRoots": roots, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true}
			appServerTestNotify(t, previous, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
				"model": "batch-model", "effort": "high", "activePermissionProfile": nil, "sandboxPolicy": policy,
			}})
			u.panes = new(nativePanePersistence)
			u.switchOrchestratedThread("child")
			drainOrchestrateWork(t, u)
			read := btwTestRequest(t, w, "thread/read", "child")
			btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": "child", "cwd": b.Cwd}})))
			var resume struct {
				ID     int            `json:"id"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(w.Bytes(), &resume); err != nil {
				t.Fatal(err)
			}
			w.Reset()
			if resume.Params["sandbox"] != "read-only" || resume.Params["permissions"] != nil {
				t.Fatal("resume used invocation permissions", resume.Params)
			}
			btwTestReply(t, u, btwTestRPC{ID: resume.ID}, string(mustMarshalJSON(map[string]any{
				"thread": map[string]any{"id": "child", "cwd": b.Cwd}, "model": "batch-model", "reasoningEffort": "high",
				"activePermissionProfile": nil, "sandbox": map[string]any{"type": "readOnly"},
			})))
			var correction int
			for _, req := range appServerDrainRequests[struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}](t, w) {
				if req.Method == "thread/settings/update" {
					correction = req.ID
					if len(roots) == 0 {
						req.Params["sandboxPolicy"].(map[string]any)["writableRoots"] = roots
					}
					if !reflect.DeepEqual(req.Params["sandboxPolicy"], policy) {
						t.Fatal("resume lost the latest custom policy", req.Params)
					}
				} else {
					btwTestReply(t, u, btwTestRPC{ID: req.ID}, `{"data":[]}`)
				}
			}
			view := u.navigation.views["child"]
			if correction == 0 || !view.settings.pending() || len(u.navigation.resumes) != 1 {
				t.Fatal("resume admitted input before policy restoration")
			}
			state, err := view.panes.read(b.Cwd, "child")
			var retained map[string]any
			if err == nil && json.Unmarshal([]byte(state.Settings.SandboxPolicy), &retained) == nil && len(roots) == 0 {
				retained["writableRoots"] = roots
			}
			if err != nil || !reflect.DeepEqual(retained, policy) {
				t.Fatal("resume hydration overwrote retained policy", state, err)
			}
			for _, req := range resumeTestRequests(t, w) {
				if req.Method != "thread/list" {
					t.Fatal("unexpected remaining history request", req.Method)
				}
				btwTestReply(t, u, btwTestRPC{ID: req.ID}, `{"data":[]}`)
			}
			btwTestReply(t, u, btwTestRPC{ID: correction}, `{}`)
			if !view.settings.pending() {
				t.Fatal("enqueue acknowledgement claimed policy restoration")
			}
			appServerTestNotify(t, view, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
				"model": "batch-model", "effort": "high", "activePermissionProfile": nil, "sandboxPolicy": map[string]any{"type": "readOnly"},
			}})
			state, err = view.panes.read(b.Cwd, "child")
			if err != nil || !view.settings.pending() || state.Settings.SandboxPolicy == "" {
				t.Fatal("resume-default notification released input or replaced the saved policy")
			}
			appServerTestNotify(t, view, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
				"model": "batch-model", "effort": "high", "activePermissionProfile": nil, "sandboxPolicy": policy,
			}})
			u.navigation.completeResumes()
			if view.settings.pending() || len(u.navigation.resumes) != 0 {
				t.Fatal("host-confirmed policy did not admit the batch")
			}
			// Switching to a named profile retires the saved custom policy.
			appServerTestNotify(t, view, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
				"model": "batch-model", "effort": "high", "activePermissionProfile": map[string]any{"id": ":read-only"},
			}})
			state, err = view.panes.read(b.Cwd, "child")
			if err != nil || state.Settings.SandboxPolicy != "" {
				t.Fatal("named profile retained superseded custom policy")
			}
		})
	}
}

func TestAppServerOrchestrateResumeUnreadableSettings(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u, w, b := retainedOrchestratedUI(t)
	u.panes = new(nativePanePersistence)
	path, err := nativePaneStatePath(b.Cwd, "child")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "{invalid")
	u.switchOrchestratedThread("child")
	drainOrchestrateWork(t, u)
	read := btwTestRequest(t, w, "thread/read", "child")
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": "child", "cwd": b.Cwd}})))
	if w.Len() != 0 || len(u.navigation.resumes) != 0 || !strings.Contains(u.notice, "read batch settings") {
		t.Fatal("unreadable batch settings resumed with invocation permissions", w.String(), u.notice)
	}
}

func retainedOrchestratedUI(t *testing.T) (*appServerUI, *appServerTestInput, orchestrate.Batch) {
	t.Helper()
	old, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, old, launch, `{"turn":{"id":"old-turn"}}`)
	store := old.proxy.orchestration.store
	batches, err := store.Snapshot(old.session.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	b := batches[0]
	// Resume must admit advanced commits and dirty unfinished inputs.
	writeTestFile(t, filepath.Join(b.Cwd, "file"), "committed batch change")
	gitTestCommit(t, b.Cwd)
	writeTestFile(t, filepath.Join(b.Cwd, "unfinished"), "keep")
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := journalRun{Directory: store.Directory, Workspace: old.session.cwd, Main: "main"}
	for thread, cwd := range map[string]string{"main": old.session.cwd, "child": b.Cwd} {
		ctx, release, err := replay.beginSession(t.Context(), thread, "")
		if err != nil {
			t.Fatal(err)
		}
		err = old.proxy.journals.initialize(ctx, replay, cwd, thread, "/root", "")
		if err == nil {
			err = old.proxy.journals.bindIdentity(ctx, replay, cwd, thread, "", "/root", true)
		}
		if err == nil {
			err = old.proxy.journals.bindRun(ctx, replay, cwd, thread, run)
		}
		release()
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	u, w := resumeOrchestrationMain(t, store, old.session.cwd, reopened)
	drainOrchestrateWork(t, u)
	w.Reset()
	return u, w, b
}

func resumeBatchResponses(t *testing.T, u *appServerUI, w *appServerTestInput, b orchestrate.Batch, active bool) {
	t.Helper()
	read := btwTestRequest(t, w, "thread/read", "child")
	thread := map[string]any{"id": "child", "cwd": b.Cwd}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
	resume := btwTestRequest(t, w, "thread/resume", "child")
	if _, ok := resume.Params.Config["model"]; ok {
		t.Fatal("Main model override replaced batch settings")
	}
	status := "completed"
	if active {
		status = "inProgress"
	}
	thread["turns"] = []any{map[string]any{"id": "history-turn", "status": status, "items": []any{map[string]any{"id": "answer", "type": "agentMessage", "text": "Retained batch answer", "channel": "final_answer"}}}}
	btwTestReply(t, u, resume, string(mustMarshalJSON(map[string]any{"thread": thread, "model": "batch-model", "reasoningEffort": "high"})))
	if u.viewedUI() != u || len(u.navigation.resumes) != 1 {
		t.Fatal("resume bypassed history admission")
	}
	// Shared controller requests its model catalog and both live/archived rosters.
	for i := 0; i < 2; i++ {
		lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
		w.Reset()
		for _, line := range lines {
			var req btwTestRPC
			if err := json.Unmarshal(line, &req); err != nil {
				t.Fatal(err)
			}
			if req.Method != "model/list" && req.Method != "thread/list" {
				t.Fatalf("unexpected restore request %s", line)
			}
			btwTestReply(t, u, req, `{"data":[]}`)
		}
	}
}

func TestAppServerOrchestrateResumeView(t *testing.T) {
	u, w, b := retainedOrchestratedUI(t)
	u.resumeConfig = map[string]any{"model": "main-only", "model_reasoning_effort": "low", "service_tier": "flex"}
	u.draft = "Main draft"
	u.agents.rosterSelected = "/Orchestration/batch"
	u.shell.focus = 3
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	// Selection does not block the UI on run storage or expose an unadmitted editor.
	if w.Len() != 0 || u.viewedUI() != u {
		t.Fatal("selection skipped storage admission")
	}
	drainOrchestrateWork(t, u)
	if !u.orchestrateBusy() {
		t.Fatal("pending resume did not protect departure")
	}
	orchestratePromptQuestion(t, u, false)
	if u.navigation.views["child"].questionCount() != 0 {
		t.Fatal("source prompt exposed input before resume admission")
	}
	// A second selection shares the same host resume.
	u.switchOrchestratedThread("child")
	resumeBatchResponses(t, u, w, b, false)
	v := u.viewedUI()
	if v.shell.focus != 3 {
		t.Fatal("retained roster pick did not focus Agents")
	}
	if v.questionCount() != 1 {
		t.Fatal("resume lost the buffered source prompt")
	}
	if v == u || v.model != "batch-model" || v.restoring != nil || len(u.navigation.resumes) != 0 || u.draft != "Main draft" || u.orchestrateThreads["child"].turn != "" {
		t.Fatal("resume lost settings, drafts or idle identity")
	}
	var frame bytes.Buffer
	if err := v.paint(&frame, 120, 40); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frame.String(), "main › batch") || !strings.Contains(frame.String(), "Retained batch answer") {
		t.Fatal("retained history did not reach its renderer")
	}
	if got, err := os.ReadFile(filepath.Join(b.Cwd, "unfinished")); err != nil || string(got) != "keep" {
		t.Fatal("resume changed unfinished work", err)
	}
	u.switchOrchestratedThread("main")
	if got := u.session.paths["child"]; got != "/orchestrate/child" {
		t.Fatalf("resumed root kept placeholder identity: %q", got)
	}
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: u.session.agents})
	u.orchestrationRoster()
	for _, row := range u.agents.roster() {
		if row.agent.Name == appServerPlaceholder("child") || strings.HasPrefix(row.agent.Name, "/orchestrate/") {
			t.Fatalf("resumed batch leaked into native roster: %q", row.agent.Name)
		}
	}
	u.switchOrchestratedThread("child")
	if strings.Contains(w.String(), "thread/resume") || strings.Contains(w.String(), "turn/start") {
		t.Fatal("subscribed switching replayed effects")
	}
}

func TestAppServerOrchestrateResumeFollowup(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			u, w, b := retainedOrchestratedUI(t)
			c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", followup: true, callID: "resumed-message", input: orchestrateSpawnInput{Message: "Continue retained work"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			resumeBatchResponses(t, u, w, b, active)
			drainOrchestrateWork(t, u)
			method := "turn/start"
			if active {
				method = "turn/steer"
			}
			wire := w.String()
			req := btwTestRequest(t, w, method, "child")
			if active && !strings.Contains(wire, `"expectedTurnId":"history-turn"`) {
				t.Fatal("steer lost current host turn identity")
			}
			response := `{"turn":{"id":"next-turn"}}`
			if active {
				response = `{"turnId":"history-turn"}`
			}
			orchestrateTestReply(t, u, req, response)
			if r := <-c.reply; r.err != nil || r.delivery == nil || r.delivery.State != "delivered" {
				t.Fatal("resumed delivery failed", r.err)
			}
			if u.viewedUI() != u {
				t.Fatal("message unexpectedly switched the view")
			}
		})
	}
}

func TestAppServerOrchestrateResumeAdmission(t *testing.T) {
	for _, mode := range []string{"branch", "settings-cwd", "resume-thread", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			u, w, b := retainedOrchestratedUI(t)
			if mode == "branch" {
				gitTestRun(t, b.Cwd, "checkout", "-q", "-b", "unrelated")
			}
			u.switchOrchestratedThread("child")
			drainOrchestrateWork(t, u)
			if mode != "branch" {
				req := btwTestRequest(t, w, "thread/read", "child")
				cwd, id := b.Cwd, "child"
				if mode == "resume-thread" {
					btwTestReply(t, u, req, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": id, "cwd": cwd}})))
					req = btwTestRequest(t, w, "thread/resume", "child")
					id = "wrong"
				} else if mode == "settings-cwd" {
					cwd = t.TempDir()
				}
				if mode == "rejected" {
					orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, req.ID))
				} else {
					btwTestReply(t, u, req, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": id, "cwd": cwd}})))
				}
			}
			if u.viewedUI() != u || len(u.navigation.resumes) != 0 || len(u.navigation.views) != 1 || !strings.Contains(u.notice, "Orchestration resume:") || w.Len() != 0 {
				t.Fatal("resume failure admitted a view or input", u.notice, w.String())
			}
		})
	}
}

func TestAppServerOrchestrateResumeDiffAdmission(t *testing.T) {
	for _, preincluded := range []bool{false, true} {
		for _, message := range []bool{false, true} {
			t.Run(fmt.Sprintf("preincluded=%t/followup=%t", preincluded, message), func(t *testing.T) {
				u, w, b := retainedOrchestratedUI(t)
				auto, stop := newAutoLiveDiff(t.Context(), u.proxy.replayStore.directory)
				defer stop()
				auto.enable()
				u.proxy.autoLiveDiff, u.shell.auto = auto, auto
				sub := auto.events.subscribe()
				auto.includeThread(u.session.cwd, "main", true)
				if preincluded {
					auto.includeThread(b.Cwd, "child", false)
				}
				// The producer published this scope before the child view existed.
				for len(sub.events) > 0 {
					u.applyOrchestrationDiff(t.Context(), <-sub.events)
				}
				liveDiffScopeCapture(t, u.proxy.replayStore, b.Cwd, "child", "saved-edit", filepath.Join(b.Cwd, "file"), "before", "after")
				if message {
					u.startOrchestratedChild(&orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", followup: true, callID: "diff-resume", input: orchestrateSpawnInput{Message: "Continue after Diff"}, reply: make(chan orchestrateResult, 1)})
				} else {
					u.switchOrchestratedThread("child")
				}
				drainOrchestrateWork(t, u)
				v := u.navigation.views["child"]
				if v.restoring == nil || u.viewedUI() != u {
					t.Fatal("resume skipped consuming Diff admission")
				}
				resumeBatchResponses(t, u, w, b, false)
				drainOrchestrateWork(t, u)
				if v.restoring != nil || len(u.navigation.resumes) != 0 || len(v.shell.diff.data.files()) != 1 {
					t.Fatal("background Diff hydration did not finish resume")
				}
				if message {
					btwTestRequest(t, w, "turn/start", "child")
				} else if u.viewedUI() != v {
					t.Fatal("hydrated child did not become viewable")
				}
			})
		}
	}
}

func TestAppServerOrchestrateResumeDefaultEffortRejection(t *testing.T) {
	u, w, b := retainedOrchestratedUI(t)
	u.switchOrchestratedThread("child")
	drainOrchestrateWork(t, u)
	read := btwTestRequest(t, w, "thread/read", "child")
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeTestFile(t, rollout, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"thread_settings_applied\",\"thread_settings\":{\"model\":\"batch-model\"}}}\n")
	thread := map[string]any{"id": "child", "cwd": b.Cwd, "path": rollout}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
	resume := btwTestRequest(t, w, "thread/resume", "child")
	btwTestReply(t, u, resume, string(mustMarshalJSON(map[string]any{"thread": thread, "model": "batch-model", "reasoningEffort": "high", "collaborationMode": map[string]any{"mode": "default", "settings": map[string]any{"model": "batch-model", "reasoning_effort": "high"}}})))
	var update btwTestRPC
	for _, req := range appServerDrainRequests[btwTestRPC](t, w) {
		if req.Method == "thread/settings/update" {
			update = req
		}
	}
	if update.Method == "" {
		t.Fatal("saved default did not reach corrective host update")
	}
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"cannot clear"}}`, update.ID))
	if u.viewedUI() != u || len(u.navigation.resumes) != 0 || len(u.navigation.views) != 1 || u.orchestrateBusy() || !strings.Contains(u.notice, "cannot clear") {
		t.Fatal("reasoning rejection stranded resume", u.notice)
	}
}

func TestAppServerOrchestrateCycleRetained(t *testing.T) {
	u, w, b := retainedOrchestratedUI(t)
	u.draft = "Main draft"
	u.cycleOrchestratedThread(1)
	drainOrchestrateWork(t, u)
	resumeBatchResponses(t, u, w, b, false)
	if u.viewedUI().thread != "child" || u.draft != "Main draft" {
		t.Fatal("cycling skipped the retained thread or changed Main's draft")
	}
	u.cycleOrchestratedThread(-1)
	if u.viewedUI() != u {
		t.Fatal("reverse cycle did not restore Main")
	}
}
