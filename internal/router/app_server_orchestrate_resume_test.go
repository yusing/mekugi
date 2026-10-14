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
			var u *appServerUI
			var w *appServerTestInput
			var b orchestrate.Batch
			if rootCount == 0 {
				u, w = idleOrchestration(t)
				b = u.orchestrateThreads["child"].batch
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": appServerThreadInfo{ID: "native", ParentThreadID: "child"}})
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "native", "turn": map[string]any{"id": "old-native", "status": "completed"}})
				btwTestReply(t, u, btwTestRequest(t, w, "thread/read", "native"), `{"thread":{"id":"native","parentThreadId":"child"}}`)
				appServerTestKeys(t, u, "/clear\r")
				start := btwTestRequest(t, w, "thread/start", "")
				btwTestReply(t, u, start, fmt.Sprintf(`{"thread":{"id":"other","cwd":%q}}`, u.session.cwd))
				resumeTestSettle(t, u, w)
				appServerTestKeys(t, u, "/resume main\r")
				back := resumeTestPrepared(t, u, w)
				btwTestReply(t, u, btwTestRPC{ID: back.ID}, fmt.Sprintf(`{"thread":{"id":"main","cwd":%q}}`, u.session.cwd))
				resumeTestSettle(t, u, w)
				drainOrchestrateWork(t, u)
			} else {
				u, w, b = retainedOrchestratedUI(t)
			}
			// Save a post-launch policy through the ordinary host-settings consumer.
			previous, _ := appliedSettingsTestUI(t, b.Cwd, "child")
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
			var follow *orchestrateCommand
			if rootCount == 0 {
				follow = &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "custom-resume", input: orchestrateSpawnInput{Message: "Continue"}, reply: make(chan orchestrateResult, 1)}
				u.startOrchestratedChild(follow)
			} else {
				u.switchOrchestratedThread("child")
			}
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
			if rootCount == 0 {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "resumed-turn"}})
				if view.turn != "resumed-turn" || u.orchestrateThreads["child"].turn != "" {
					t.Fatal("pending resume lost lifecycle input or admitted dispatch")
				}
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "resumed-turn", "status": "interrupted"}})
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "native", "turnId": "native-turn", "item": appServerItem{ID: "native-output", Type: "commandExecution", Command: "echo native-output", Status: "completed"}})
				approvalTestCommand(t, u, "native-approval", map[string]any{"threadId": "native", "turnId": "native-turn"})
				read := btwTestRequest(t, w, "thread/read", "native")
				btwTestReply(t, u, read, `{"thread":{"id":"native","parentThreadId":"child"}}`)
				if len(view.approvals.pending) != 1 || len(u.approvals.pending) != 0 || !view.settings.pending() {
					t.Fatal("authenticated descendant approval lost its restoring owner")
				}
				if !strings.Contains(strings.Join(view.agents.renderFeed(90, 60).lines, "\n"), "native-output") {
					t.Fatal("authenticated descendant activity was discarded")
				}
			}
			appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
				"model": "batch-model", "effort": "high", "activePermissionProfile": nil, "sandboxPolicy": map[string]any{"type": "readOnly"},
			}})
			state, err = view.panes.read(b.Cwd, "child")
			if err != nil || !view.settings.pending() || state.Settings.SandboxPolicy == "" {
				t.Fatal("resume-default notification released input or replaced the saved policy")
			}
			appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
				"model": "batch-model", "effort": "high", "activePermissionProfile": nil, "sandboxPolicy": policy,
			}})
			u.navigation.completeResumes()
			if view.settings.pending() || len(u.navigation.resumes) != 0 {
				t.Fatal("host-confirmed policy did not admit the batch")
			}
			if follow != nil {
				if !u.openApprovals() {
					t.Fatal("restoration lost the descendant approval")
				}
				if handled, err := u.approvalKey("\r"); !handled || err != nil || !strings.Contains(w.String(), `"id":"native-approval","result":{"decision":"accept"}`) {
					t.Fatal("restored approval could not answer its host request", err, w.String())
				}
				w.Reset()
				drainOrchestrateWork(t, u)
				orchestrateTestReply(t, u, btwTestRequest(t, w, "turn/start", "child"), `{"turn":{"id":"next"}}`)
				if r := <-follow.reply; r.err != nil {
					t.Fatal(r.err)
				}
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

func TestAppServerOrchestrateResumeNamedProfile(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			u, w, b := retainedOrchestratedUI(t)
			workspace := u.session.cwd
			// Ordinary session navigation must retain the batch's observed profile.
			appServerTestKeys(t, u, "/resume child\r")
			preflight := resumeTestOne(t, w, "thread/read")
			btwTestReply(t, u, btwTestRPC{ID: preflight.ID}, string(mustMarshalJSON(map[string]any{"thread": appServerThreadInfo{ID: "child", Cwd: b.Cwd}})))
			ordinary := resumeTestOne(t, w, "thread/resume")
			btwTestReply(t, u, btwTestRPC{ID: ordinary.ID}, string(mustMarshalJSON(map[string]any{
				"thread": appServerThreadInfo{ID: "child", Cwd: b.Cwd}, "model": "batch-model", "reasoningEffort": "high", "activePermissionProfile": map[string]any{"id": ":read-only"},
			})))
			resumeTestSettle(t, u, w)
			drainOrchestrateWork(t, u)
			appServerTestKeys(t, u, "/resume main\r")
			back := resumeTestPrepared(t, u, w)
			btwTestReply(t, u, btwTestRPC{ID: back.ID}, fmt.Sprintf(`{"thread":{"id":"main","cwd":%q},"model":"model"}`, workspace))
			resumeTestSettle(t, u, w)
			drainOrchestrateWork(t, u)
			c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "named-profile", input: orchestrateSpawnInput{Message: "Continue"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(c)
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
			if resume.Params["permissions"] != ":read-only" {
				t.Fatal("resume widened the retained profile to invocation permissions", resume.Params)
			}
			profile := ":read-only"
			if !confirmed {
				profile = ":danger-full-access"
			}
			btwTestReply(t, u, btwTestRPC{ID: resume.ID}, string(mustMarshalJSON(map[string]any{
				"thread": map[string]any{"id": "child", "cwd": b.Cwd}, "model": "batch-model", "reasoningEffort": "high", "activePermissionProfile": map[string]any{"id": profile},
			})))
			if confirmed {
				resumeTestSettle(t, u, w)
				drainOrchestrateWork(t, u)
				orchestrateTestReply(t, u, btwTestRequest(t, w, "turn/start", "child"), `{"turn":{"id":"next"}}`)
			}
			if r := <-c.reply; (r.err == nil) != confirmed || w.Len() != 0 {
				t.Fatal("follow-up did not require the retained permission profile", r.err, w.String())
			}
		})
	}
}

func TestAppServerOrchestrateResumeUnreadableSettings(t *testing.T) {
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

func TestAppServerOrchestrateResumeMissingPermissions(t *testing.T) {
	for _, missing := range []string{"file", "permissions"} {
		t.Run(missing, func(t *testing.T) {
			u, w, b := retainedOrchestratedUI(t)
			path, err := nativePaneStatePath(b.Cwd, "child")
			if err != nil {
				t.Fatal(err)
			}
			if missing == "file" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				state, err := u.panes.read(b.Cwd, "child")
				if err != nil {
					t.Fatal(err)
				}
				state.Settings.PermissionProfile = ""
				writeTestFile(t, path, string(mustMarshalJSON(state)))
			}
			c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "missing-permissions", input: orchestrateSpawnInput{Message: "Continue"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			read := btwTestRequest(t, w, "thread/read", "child")
			btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": map[string]any{"id": "child", "cwd": b.Cwd}})))
			select {
			case r := <-c.reply:
				if r.err == nil || !strings.Contains(r.err.Error(), "batch permissions") || w.Len() != 0 {
					t.Fatal("missing exact permissions admitted batch input", r.err, w.String())
				}
			default:
				t.Fatal("missing exact permissions requested host resume", w.String())
			}
		})
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
	retainOrchestratedTestPermissions(t, u, b)
	return u, w, b
}

func retainOrchestratedTestPermissions(t *testing.T, u *appServerUI, b orchestrate.Batch) {
	t.Helper()
	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateDir)
	previous, _ := appliedSettingsTestUI(t, b.Cwd, "child")
	previous.navigation = &orchestrateNavigation{owner: u}
	appServerTestNotify(t, previous, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
		"model": "batch-model", "effort": "high", "activePermissionProfile": map[string]any{"id": ":read-only"},
	}})
	u.panes = new(nativePanePersistence)
}

func resumeBatchResponses(t *testing.T, u *appServerUI, w *appServerTestInput, b orchestrate.Batch, active bool) {
	t.Helper()
	read := btwTestRequest(t, w, "thread/read", "child")
	thread := map[string]any{"id": "child", "cwd": b.Cwd}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
	resume := btwTestRequest(t, w, "thread/resume", "child")
	if model, ok := resume.Params.Config["model"]; ok && model != "batch-model" {
		t.Fatal("Main model override replaced batch settings")
	}
	status := "completed"
	if active {
		status = "inProgress"
	}
	thread["turns"] = []any{map[string]any{"id": "history-turn", "status": status, "items": []any{map[string]any{"id": "answer", "type": "agentMessage", "text": "Retained batch answer", "channel": "final_answer"}}}}
	btwTestReply(t, u, resume, string(mustMarshalJSON(map[string]any{"thread": thread, "model": "batch-model", "reasoningEffort": "high", "activePermissionProfile": map[string]any{"id": ":read-only"}})))
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
	// Resume must admit advanced commits and dirty unfinished inputs.
	writeTestFile(t, filepath.Join(b.Cwd, "file"), "committed batch change")
	gitTestCommit(t, b.Cwd)
	writeTestFile(t, filepath.Join(b.Cwd, "unfinished"), "keep")
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
			c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "resumed-message", input: orchestrateSpawnInput{Message: "Continue retained work"}, reply: make(chan orchestrateResult, 1)}
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
					u.startOrchestratedChild(&orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "diff-resume", input: orchestrateSpawnInput{Message: "Continue after Diff"}, reply: make(chan orchestrateResult, 1)})
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
	previous, _ := appliedSettingsTestUI(t, b.Cwd, "child")
	previous.navigation = &orchestrateNavigation{owner: u}
	appServerTestNotify(t, previous, "thread/settings/updated", map[string]any{"threadId": "child", "threadSettings": map[string]any{
		"model": "batch-model", "effort": nil, "activePermissionProfile": map[string]any{"id": ":read-only"},
	}})
	u.switchOrchestratedThread("child")
	drainOrchestrateWork(t, u)
	read := btwTestRequest(t, w, "thread/read", "child")
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeTestFile(t, rollout, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"thread_settings_applied\",\"thread_settings\":{\"model\":\"batch-model\"}}}\n")
	thread := map[string]any{"id": "child", "cwd": b.Cwd, "path": rollout}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
	resume := btwTestRequest(t, w, "thread/resume", "child")
	btwTestReply(t, u, resume, string(mustMarshalJSON(map[string]any{"thread": thread, "model": "batch-model", "reasoningEffort": "high", "activePermissionProfile": map[string]any{"id": ":read-only"}, "collaborationMode": map[string]any{"mode": "default", "settings": map[string]any{"model": "batch-model", "reasoning_effort": "high"}}})))
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

func TestAppServerOrchestrateResumeDescendantDiff(t *testing.T) {
	u, w, b := retainedOrchestratedUI(t)
	auto, stop := newAutoLiveDiff(t.Context(), u.proxy.replayStore.directory)
	t.Cleanup(stop)
	auto.enable()
	u.proxy.autoLiveDiff, u.shell.auto = auto, auto
	sub := auto.events.subscribe()
	for _, thread := range []string{"child", "native", "foreign"} {
		auto.includeThread(b.Cwd, thread, thread == "child")
	}
	for len(sub.events) > 0 {
		u.applyOrchestrationDiff(t.Context(), <-sub.events)
	}
	u.switchOrchestratedThread("child")
	drainOrchestrateWork(t, u)
	read := btwTestRequest(t, w, "thread/read", "child")
	root := map[string]any{"id": "child", "cwd": b.Cwd}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": root})))
	resume := btwTestRequest(t, w, "thread/resume", "child")
	btwTestReply(t, u, resume, string(mustMarshalJSON(map[string]any{"thread": root, "model": "batch-model", "reasoningEffort": "high", "activePermissionProfile": map[string]any{"id": ":read-only"}})))
	native := map[string]any{"id": "native", "cwd": b.Cwd, "parentThreadId": "child", "agentNickname": "worker"}
	for w.Len() > 0 {
		for _, request := range appServerDrainRequests[btwTestRPC](t, w) {
			switch request.Method {
			case "thread/list":
				btwTestReply(t, u, request, string(mustMarshalJSON(map[string]any{"data": []any{native}})))
			case "thread/read":
				btwTestReply(t, u, request, string(mustMarshalJSON(map[string]any{"thread": native})))
			case "model/list":
				btwTestReply(t, u, request, `{"data":[]}`)
			default:
				t.Fatal("unexpected request", request.Method)
			}
		}
	}
	if err := u.tickOrchestratedViews(u); err != nil {
		t.Fatal(err)
	}
	v := u.navigation.views["child"]
	if v.restoring != nil || len(u.navigation.resumes) != 0 || u.viewedUI() != v {
		t.Fatal("descendant scope blocked restored batch admission", v.status)
	}
	if scope := v.shell.diff.scope.Workspaces[b.Cwd]; !scope["native"] || scope["foreign"] || u.shell.diff.scope.Workspaces[b.Cwd]["native"] {
		t.Fatal("restored descendant Diff scope crossed roots or stayed excluded", scope)
	}
}
