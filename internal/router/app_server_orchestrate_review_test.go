package router

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vt "github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func workflowUI(t *testing.T, checks ...string) (*appServerUI, *appServerTestInput) {
	t.Helper()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestratePendingWorkflow(t, replay, orchestrateVCSWorkspace(t, "git"), nil, &orchestrateSpawnInput{TaskName: "batch", Message: "bounded implementation", RequiredChecks: checks})
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"implement"}}`)
	return u, u.client.Input.(*appServerTestInput)
}
func workflowCompleted(t *testing.T, u *appServerUI, thread, turn, text string) {
	t.Helper()
	if text != "" {
		raw, _ := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": turn + "-result", "type": "agentMessage", "text": text}}})
		orchestrateTestMessage(t, u, string(raw))
	}
	raw, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}}})
	orchestrateTestMessage(t, u, string(raw))
	workflowDefaultConfig(t, u)
}

// workflowTake removes the first buffered request matching method, thread, and,
// when set, workflow kind. Concurrent reviews leave several requests buffered.
func workflowTake(t *testing.T, u *appServerUI, w *appServerTestInput, method, thread string, kind orchestrate.WorkflowDispatchKind) (btwTestRPC, map[string]any) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
	for i, line := range lines {
		var r btwTestRPC
		var raw struct {
			Params map[string]any `json:"params"`
		}
		if len(line) == 0 || json.Unmarshal(line, &r) != nil || json.Unmarshal(line, &raw) != nil || r.Method != method || r.Params.ThreadID != thread {
			continue
		}
		if request, ok := u.orchestrateRequests[fmt.Sprint(r.ID)]; kind != "" && (!ok || request.review == nil || request.review.dispatch.Kind != kind) {
			continue
		}
		rest := append(lines[:i:i], lines[i+1:]...)
		w.Reset()
		if len(rest) != 0 {
			w.Write(append(bytes.Join(rest, []byte{'\n'}), '\n'))
		}
		return r, raw.Params
	}
	t.Fatalf("no %s %s request on %q in %s", kind, method, thread, w.String())
	return btwTestRPC{}, nil
}
func workflowDefaultConfig(t *testing.T, u *appServerUI) {
	t.Helper()
	if roles := u.proxy.orchestration.reviewers; roles.Correctness != "" || roles.Simplification != "" {
		return
	}
	w := u.client.Input.(*appServerTestInput)
	for strings.Contains(w.String(), `"method":"config/read"`) {
		request, params := workflowTake(t, u, w, "config/read", "", "")
		if params["cwd"] != u.session.cwd || params["includeLayers"] != true {
			t.Fatal("review config must use coordinator source layers", params)
		}
		orchestrateTestReply(t, u, request, `{"layers":[]}`)
	}
}

// workflowReviewThread acknowledges the scratch-only review thread and turn.
func workflowReviewThread(t *testing.T, u *appServerUI, w *appServerTestInput, purpose orchestrate.ReviewPurpose, thread string) {
	t.Helper()
	kind := orchestrate.WorkflowDispatchKind(purpose)
	start, params := workflowTake(t, u, w, "thread/start", "", kind)
	profile, _ := params["permissions"].(string)
	if params["approvalPolicy"] != "never" || params["sandbox"] != nil || !strings.HasPrefix(profile, "mekugi-review-") {
		t.Fatal("review launch does not use scratch-only permissions", params)
	}
	child := u.orchestrateThreads["child"]
	response, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "cwd": child.batch.Cwd}, "sandbox": map[string]any{"type": "readOnly"}, "activePermissionProfile": map[string]any{"id": profile}, "approvalPolicy": "never"})
	orchestrateTestReply(t, u, start, string(response))
	turn, params := workflowTake(t, u, w, "turn/start", thread, kind)
	if params["outputSchema"] == nil || params["sandboxPolicy"] != nil || params["permissions"] != profile {
		t.Fatal("review turn lost schema or scratch-only policy", params)
	}
	assignment := params["input"].([]any)[0].(map[string]any)["text"].(string)
	base := child.batch.Base
	for _, previous := range child.batch.Workflow.PreviousReviews {
		if previous.Result.Purpose == purpose {
			base = previous.Result.Revision
		}
		raw, _ := json.Marshal(&previous)
		if previous.Result.Purpose == purpose && !strings.Contains(assignment, "Prior review (retained verdict and dispatch provenance): "+string(raw)) {
			t.Fatal("review turn lost prior verdict evidence", assignment)
		}
	}
	if !strings.Contains(assignment, "Review candidate "+child.batch.Workflow.Candidate.Revision+" against baseline "+base+" for "+string(purpose)) {
		t.Fatal("review turn used the wrong candidate comparison", assignment)
	}
	for _, receipt := range child.batch.Workflow.Checks {
		raw, _ := json.Marshal(receipt)
		if !strings.Contains(assignment, "Runtime validation receipt (host command/exec): "+string(raw)) {
			t.Fatal("review turn omitted a runtime receipt", assignment)
		}
	}
	orchestrateTestReply(t, u, turn, fmt.Sprintf(`{"turn":{"id":%q}}`, thread+"-turn"))
}
func workflowReview(t *testing.T, u *appServerUI, w *appServerTestInput, purpose orchestrate.ReviewPurpose, verdict orchestrate.ReviewVerdict) {
	t.Helper()
	child := u.orchestrateThreads["child"]
	thread := fmt.Sprintf("%s-%d", purpose, child.batch.Workflow.Generation)
	turnID := thread + "-turn"
	if work := child.workflowWork[orchestrate.WorkflowDispatchKind(purpose)]; work != nil && work.dispatch.ThreadID != "" {
		thread = work.dispatch.ThreadID
		if u.navigation.views[thread].workflowReview.host != u.client {
			resume, params := workflowTake(t, u, w, "thread/resume", thread, "")
			response, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "cwd": child.batch.Cwd}, "activePermissionProfile": map[string]any{"id": params["permissions"]}, "approvalPolicy": "never"})
			orchestrateTestReply(t, u, resume, string(response))
		}
		start, params := workflowTake(t, u, w, "turn/start", thread, "")
		assignment := params["input"].([]any)[0].(map[string]any)["text"].(string)
		if len(child.batch.Workflow.PreviousReviews) == 0 && !strings.Contains(assignment, child.batch.Workflow.Assignment.Scope) {
			t.Fatal("full review omitted current accepted scope", assignment)
		}
		if len(child.batch.Workflow.PreviousReviews) > 0 && (!strings.Contains(assignment, "Correction review:") || strings.Contains(assignment, "Candidate source is read-only")) {
			t.Fatal("follow-up did not reuse retained instructions", assignment)
		}
		orchestrateTestReply(t, u, start, fmt.Sprintf(`{"turn":{"id":%q}}`, turnID))
	} else {
		workflowReviewThread(t, u, w, purpose, thread)
	}
	result := orchestrate.ReviewResult{Revision: child.batch.Workflow.Candidate.Revision, Purpose: purpose, Verdict: verdict, Findings: []orchestrate.ReviewFinding{}}
	if verdict == orchestrate.VerdictFindings {
		result.Findings = []orchestrate.ReviewFinding{{ID: "F1", Detail: "change required"}}
	}
	raw, _ := json.Marshal(result)
	workflowCompleted(t, u, thread, turnID, string(raw))
}

// workflowCheck answers the next host check; an "error:" response is an RPC error.
func workflowCheck(t *testing.T, u *appServerUI, w *appServerTestInput, command, response string) {
	t.Helper()
	request, params := workflowTake(t, u, w, "command/exec", "", workflowValidation)
	child := u.orchestrateThreads["child"]
	argv, _ := params["command"].([]any)
	if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" || argv[2] != command || params["cwd"] != child.batch.Cwd || params["processId"] == nil || params["disableTimeout"] != true {
		t.Fatal("check request does not run the declared command in the checkout", params)
	}
	if message, ok := strings.CutPrefix(response, "error:"); ok {
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":%q}}`, request.ID, message))
		drainOrchestrateWork(t, u)
		return
	}
	orchestrateTestReply(t, u, request, response)
	workflowDefaultConfig(t, u)
}
func workflowNotice(t *testing.T, u *appServerUI, w *appServerTestInput) string {
	t.Helper()
	request, params := workflowTake(t, u, w, "thread/inject_items", "main", "")
	raw, _ := json.Marshal(params)
	orchestrateTestReply(t, u, request, `{}`)
	return string(raw)
}
func workflowCancel(t *testing.T, u *appServerUI) *orchestrateCommand {
	t.Helper()
	cancel := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "interrupt_agent", reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(cancel)
	drainOrchestrateWork(t, u)
	return cancel
}

func workflowVerdict(child *orchestrateChild, purpose orchestrate.ReviewPurpose) string {
	raw, _ := json.Marshal(orchestrate.ReviewResult{Revision: child.batch.Workflow.Candidate.Revision, Purpose: purpose, Verdict: orchestrate.VerdictClear, Findings: []orchestrate.ReviewFinding{}})
	return string(raw)
}

func TestWorkflowReviewAssignmentExactReceipts(t *testing.T) {
	b := orchestrate.Batch{Base: "baseline", Cwd: "/candidate", Workflow: &orchestrate.Workflow{
		Generation: 2, Candidate: &orchestrate.Candidate{Revision: "candidate"}, Report: "implementer claim",
		Checks: []orchestrate.CheckResult{
			{Generation: 2, Revision: "candidate", Cwd: "/candidate", Command: "current"},
			{Generation: 1, Revision: "candidate", Cwd: "/candidate", Command: "old generation"},
			{Generation: 2, Revision: "old revision", Cwd: "/candidate", Command: "old revision"},
			{Generation: 2, Revision: "candidate", Cwd: "/other", Command: "other checkout"},
		},
	}}
	text := workflowReviewAssignment(b, orchestrate.DispatchCorrectness)
	if strings.Count(text, "Runtime validation receipt (host command/exec):") != 1 || !strings.Contains(text, `"command":"current"`) || !strings.Contains(text, "Implementer report (claims, not host validation receipts):\nimplementer claim") {
		t.Fatal("review mixed candidate receipts or report provenance", text)
	}
	for _, previous := range [][]orchestrate.PreviousReview{nil,
		{{Result: orchestrate.ReviewResult{Revision: "previous", Purpose: orchestrate.ReviewSimplification, Verdict: orchestrate.VerdictClear}}},
		{{Result: orchestrate.ReviewResult{Revision: "previous", Purpose: orchestrate.ReviewCorrectness, Verdict: orchestrate.VerdictBlocked}}},
	} {
		b.Workflow.PreviousReviews = previous
		text = workflowReviewAssignment(b, orchestrate.DispatchCorrectness)
		if !strings.HasPrefix(text, "Review candidate candidate against baseline baseline for correctness") || strings.Contains(text, "Correction review:") {
			t.Fatal("first or incomplete purpose review lost full baseline coverage", text)
		}
	}
}

// workflowActiveReviews leaves both concurrent reviews acknowledged and running.
func workflowActiveReviews(t *testing.T, u *appServerUI, w *appServerTestInput) {
	t.Helper()
	workflowCompleted(t, u, "child", "implement", "")
	workflowReviewThread(t, u, w, orchestrate.ReviewCorrectness, "reviewer-c")
	workflowReviewThread(t, u, w, orchestrate.ReviewSimplification, "reviewer-s")
}

// workflowRestart keeps only retained state: no pending RPC, message, or handle.
func workflowRestart(t *testing.T, u *appServerUI) {
	t.Helper()
	u.proxy.orchestration.store = &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	u.orchestrateThreads = nil
	u.orchestrateRequests = nil
	u.recoverOrchestratedWorkflows(batches)
}

func workflowThreadRead(t *testing.T, u *appServerUI, w *appServerTestInput, thread, status, text string) {
	t.Helper()
	read, _ := workflowTake(t, u, w, "thread/read", thread, "")
	turn := map[string]any{"id": thread + "-turn", "status": status}
	if text != "" {
		turn["items"] = []any{map[string]any{"id": "result", "type": "agentMessage", "text": text}}
	}
	raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "cwd": u.orchestrateThreads["child"].batch.Cwd, "turns": []any{turn}}})
	orchestrateTestReply(t, u, read, string(raw))
}

func TestOrchestrateWorkflowLifecycle(t *testing.T) {
	u, w := workflowUI(t, "printf verified")
	wait := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "wait_agent", callID: "workflow-wait", reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(wait)
	drainOrchestrateWork(t, u)
	if r := <-wait.reply; r.err != nil || r.wait == nil || r.wait.State != "registered" {
		t.Fatal("workflow wait registration failed", r)
	}
	workflowCompleted(t, u, "child", "implement", "implementer claims success")
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate implementation"), Agent: "/root/batch", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"done", "accepted"} {
		if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new(state)}}); err == nil {
			t.Fatal("implementation completed before workflow readiness", state)
		}
	}
	if w.Len() != 0 && !strings.Contains(w.String(), `"method":"command/exec"`) {
		t.Fatal("reviews started before required checks", w.String())
	}
	appServerTestNotify(t, u, "command/exec/outputDelta", map[string]any{"processId": u.orchestrateThreads["child"].workflowWork[workflowValidation].dispatch.ID, "stream": "stdout", "deltaBase64": base64.StdEncoding.EncodeToString([]byte("verified"))})
	workflowCheck(t, u, w, "printf verified", `{"exitCode":0,"stdout":"","stderr":""}`)
	child := u.orchestrateThreads["child"]
	if len(child.batch.Workflow.Checks) != 1 || child.batch.Workflow.Checks[0].Output != "verified" || child.batch.Workflow.Phase != orchestrate.PhaseReview {
		t.Fatal("passed check did not start reviews", child.batch.Workflow)
	}
	// Both reviews run concurrently; either may finish first.
	workflowReviewThread(t, u, w, orchestrate.ReviewCorrectness, "reviewer-c")
	workflowReviewThread(t, u, w, orchestrate.ReviewSimplification, "reviewer-s")
	workflowCompleted(t, u, "reviewer-s", "reviewer-s-turn", workflowVerdict(child, orchestrate.ReviewSimplification))
	if child.batch.Workflow.Phase != orchestrate.PhaseReview {
		t.Fatal("one reviewer completed workflow")
	}
	workflowCompleted(t, u, "reviewer-c", "reviewer-c-turn", workflowVerdict(child, orchestrate.ReviewCorrectness))
	if child.batch.Workflow.Phase != orchestrate.PhaseReady {
		t.Fatal("workflow not ready", child.batch.Workflow)
	}
	if notice := workflowNotice(t, u, w); !strings.Contains(notice, "Workflow ready") || !strings.Contains(notice, `Implementer report:\nimplementer claims success`) {
		t.Fatal("readiness omitted the implementer report", notice)
	}
	request, params := workflowTake(t, u, w, "turn/start", "main", "")
	raw, _ := json.Marshal(params)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"wait-yield"}}`)
	if notice := string(raw); !strings.Contains(notice, "WAIT_RESULT") || !strings.Contains(notice, "ready") {
		t.Fatal("wait did not observe terminal workflow readiness", notice)
	}
	batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	if batches[0].Workflow.Outcome.State != orchestrate.DispatchAcknowledged || len(batches[0].Workflow.Checks) != 1 {
		t.Fatal("outcome or check receipt not durable")
	}
	u.driveOrchestrateWorkflows()
	drainOrchestrateWork(t, u)
	if w.Len() != 0 {
		t.Fatal("terminal outcome delivered twice", w.String())
	}
}

func TestOrchestrateWorkflowCorrections(t *testing.T) {
	u, w := workflowUI(t)
	child := u.orchestrateThreads["child"]
	submoduleCleanupGit(t, child.batch.Cwd, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "candidate")
	workflowCompleted(t, u, "child", "implement", "done")
	for round := 0; round < 3; round++ {
		workflowReview(t, u, w, orchestrate.ReviewCorrectness, orchestrate.VerdictFindings)
		workflowReview(t, u, w, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
		if round == 2 {
			break
		}
		correction, _ := workflowTake(t, u, w, "turn/start", "child", orchestrate.DispatchCorrection)
		text := correction.Params.Input[0].Text
		if !strings.Contains(text, "reviews reported findings") || !strings.Contains(text, "correctness/F1: change required") {
			t.Fatal("correction omitted the findings", text)
		}
		turn := fmt.Sprintf("correction-%d", round)
		orchestrateTestReply(t, u, correction, fmt.Sprintf(`{"turn":{"id":%q}}`, turn))
		prior := child.batch.Workflow.Candidate.Revision
		if round == 0 {
			submoduleCleanupGit(t, child.batch.Cwd, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "correction")
		}
		workflowCompleted(t, u, "child", turn, "disputed")
		batches, err := (&orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}).Snapshot(u.session.cwd, "main")
		if err != nil || len(batches[0].Workflow.PreviousReviews) != 2 || batches[0].Workflow.PreviousReviews[0].Result.Revision != prior {
			t.Fatal("correction lost the reviewed candidate on restart", batches, err)
		}
		assignment := workflowReviewAssignment(batches[0], orchestrate.DispatchSimplification)
		if strings.Contains(assignment, "Prior review (") || strings.Contains(assignment, "correctness/F1") {
			t.Fatal("follow-up repeated prior findings or crossed reviewer purposes", assignment)
		}
		for _, want := range []string{"Correction review:", "Reuse unchanged clear scope", "unresolved findings/disputes", "Review necessity and simplification", "disputed"} {
			if !strings.Contains(assignment, want) {
				t.Fatalf("restored correction review omitted %q: %s", want, assignment)
			}
		}
	}
	if u.orchestrateThreads["child"].batch.Workflow.Phase != orchestrate.PhaseNeedsDecision {
		t.Fatal("correction limit not enforced")
	}
	notice := workflowNotice(t, u, w)
	for _, want := range []string{"after two correction rounds", "correctness/F1: change required", `Implementer report:\ndisputed`, "Send a follow-up to continue the workflow, or interrupt to cancel it."} {
		if !strings.Contains(notice, want) {
			t.Fatalf("blocker notice omits %q: %s", want, notice)
		}
	}
	workflowRestart(t, u)
	drainOrchestrateWork(t, u)
	if strings.Contains(w.String(), `"method":"turn/start"`) || strings.Contains(w.String(), `"method":"thread/start"`) {
		t.Fatal("restored prior reviews revived host work", w.String())
	}
}

func TestUISnapshotOrchestrateCheckOutcomes(t *testing.T) {
	for _, test := range []string{"failed", "rpc-error", "no-exit", "canceled"} {
		t.Run(test, func(t *testing.T) {
			u, w := workflowUI(t, "printf check")
			now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
			v := u.navigation.views["child"]
			u.clock, v.clock = func() time.Time { return now }, func() time.Time { return now }
			v.view.clock, v.view.painter.Theme = v.clock, livediff.DarkTheme
			workflowCompleted(t, u, "child", "implement", "")
			child := u.orchestrateThreads["child"]
			switch test {
			case "failed":
				appServerTestNotify(t, u, "command/exec/outputDelta", map[string]any{"processId": child.workflowWork[workflowValidation].dispatch.ID, "stream": "stderr", "deltaBase64": base64.StdEncoding.EncodeToString([]byte("boom at line 7"))})
				workflowCheck(t, u, w, "printf check", `{"exitCode":2,"stdout":"","stderr":""}`)
				correction, _ := workflowTake(t, u, w, "turn/start", "child", orchestrate.DispatchCorrection)
				text := correction.Params.Input[0].Text
				if child.batch.Workflow.Phase != orchestrate.PhaseCorrection || !strings.Contains(text, "required check check-1 failed") || !strings.Contains(text, "exit code 2") || !strings.Contains(text, "boom at line 7") {
					t.Fatal("failed check was not routed to correction with its output", text)
				}
			case "rpc-error":
				workflowCheck(t, u, w, "printf check", "error:sandbox denied")
			case "no-exit":
				workflowCheck(t, u, w, "printf check", `{"stdout":"","stderr":""}`)
			case "canceled":
				cancel := workflowCancel(t, u)
				terminate, _ := workflowTake(t, u, w, "command/exec/terminate", "", workflowValidation)
				orchestrateTestReply(t, u, terminate, `{}`)
				appServerTestNotify(t, u, "command/exec/outputDelta", map[string]any{"processId": child.workflowWork[workflowValidation].dispatch.ID, "stream": "stdout", "deltaBase64": base64.StdEncoding.EncodeToString([]byte("late output"))})
				workflowCheck(t, u, w, "printf check", `{"exitCode":0,"stdout":"","stderr":""}`)
				if result := <-cancel.reply; result.err != nil || child.batch.Workflow.Phase != orchestrate.PhaseCanceled || len(child.batch.Workflow.Checks) != 0 {
					t.Fatal("late check result revived canceled validation", result, child.batch.Workflow)
				}
				if workflowUnsettled(child.batch) {
					t.Fatal("confirmed late exit left validation dispatch unsettled")
				}
			}
			uisnapshot.Assert(t, "testdata/snapshots/orchestration-check-"+test+".txt", strings.Join(v.view.renderFeed(100, 30).lines, "\n")+"\n")
			if test == "failed" || test == "canceled" {
				return
			}
			if child.batch.Workflow.Phase != orchestrate.PhaseNeedsDecision {
				t.Fatal("unconfirmed check did not stop the workflow", child.batch.Workflow)
			}
			want := map[string]string{"rpc-error": "sandbox denied", "no-exit": "host returned no exit status"}[test]
			if notice := workflowNotice(t, u, w); !strings.Contains(notice, "has no confirmed exit status") || !strings.Contains(notice, want) {
				t.Fatal("blocker notice omits check evidence", notice)
			}
		})
	}
}

func TestOrchestrateWorkflowUncommittedResultRoutesCorrection(t *testing.T) {
	u, w := workflowUI(t)
	child := u.orchestrateThreads["child"]
	if err := os.WriteFile(filepath.Join(child.batch.Cwd, "uncommitted.txt"), []byte("work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workflowCompleted(t, u, "child", "implement", "done, not committed")
	correction, _ := workflowTake(t, u, w, "turn/start", "child", orchestrate.DispatchCorrection)
	if text := correction.Params.Input[0].Text; child.batch.Workflow.Phase != orchestrate.PhaseCorrection || child.batch.Workflow.Candidate != nil || !strings.Contains(text, "not a clean committed checkout") {
		t.Fatal("uncommitted result was not routed to correction", text)
	}
	orchestrateTestReply(t, u, correction, `{"turn":{"id":"commit"}}`)
	submoduleCleanupGit(t, child.batch.Cwd, "add", "uncommitted.txt")
	submoduleCleanupGit(t, child.batch.Cwd, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "commit work")
	workflowCompleted(t, u, "child", "commit", "committed")
	if w := child.batch.Workflow; w.Phase != orchestrate.PhaseReview || w.Candidate == nil || w.Report != "committed" {
		t.Fatal("corrected commit did not enter review", w)
	}
}

func TestOrchestrateWorkflowBlockedJournalAdmission(t *testing.T) {
	u, w := workflowUI(t, "printf verified")
	child := u.orchestrateThreads["child"]
	apply := func(workspace, thread string, mutations ...journalMutation) {
		t.Helper()
		if _, err := u.proxy.journals.apply(u.ctx, u.proxy.replayStore, workspace, thread, "", mutations); err != nil {
			t.Fatal(err)
		}
	}
	apply(child.batch.Cwd, "child",
		journalMutation{Op: "add", Kind: "task", Title: new("Acceptance"), State: new("working")},
		journalMutation{Op: "add", Under: "/1", Kind: "task", Title: new("Installed host"), State: new("blocked"), Reason: new("installed-host acceptance failed")},
		journalMutation{Op: "finish", finishTurn: "implement", finishItem: "finish-item"})
	// A fresh owner must read the durable current tree, not an event notification.
	u.proxy.journals = newJournalStore()
	workflowCompleted(t, u, "child", "implement", "")
	if wf := child.batch.Workflow; wf.Phase != orchestrate.PhaseNeedsDecision || wf.Candidate != nil || len(wf.Dispatches) != 0 || wf.Report != "" {
		t.Fatal("blocked finish admitted a candidate", wf)
	}
	if notice := workflowNotice(t, u, w); !strings.Contains(notice, "/1/1: installed-host acceptance failed") || strings.Contains(notice, "Workflow ready") {
		t.Fatal("outcome lost the authored blocker", notice)
	}
	j, exists, err := readThreadJournal(u.proxy.replayStore, child.batch.Cwd, "child")
	if err != nil || !exists || j.LifecycleState != "done" || child.batch.Launch.HostStatus != "completed" {
		t.Fatal("admission changed successful host lifecycle", j.LifecycleState, child.batch.Launch, err)
	}
	// Main and a completed native descendant can retain their own blockers. The
	// implementer's owned tree, including its resolved history, is the boundary.
	apply(u.session.cwd, "main", journalMutation{Op: "add", Kind: "task", Title: new("Other work"), State: new("blocked"), Reason: new("unrelated")})
	store := u.proxy.orchestration.store
	sibling, err := store.Prepare(u.ctx, u.session.cwd, "main", "sibling")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginLaunch(u.ctx, u.session.cwd, "main", "sibling", "other work", []byte(`{}`), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		store.RecordThread(u.ctx, u.session.cwd, "main", "sibling", "sibling"),
		u.proxy.journals.initialize(u.ctx, u.proxy.replayStore, sibling.Cwd, "sibling", "/root", ""),
		u.proxy.journals.bindIdentity(u.ctx, u.proxy.replayStore, sibling.Cwd, "sibling", "", "/root", true),
		u.proxy.journals.bindRun(u.ctx, u.proxy.replayStore, sibling.Cwd, "sibling", *j.Orchestration),
		u.proxy.journals.initialize(u.ctx, u.proxy.replayStore, child.batch.Cwd, "native", "/root/worker", ""),
		u.proxy.journals.bindIdentity(u.ctx, u.proxy.replayStore, child.batch.Cwd, "native", "child", "/root/worker", true),
		u.proxy.journals.observeLifecycle(u.ctx, u.proxy.replayStore, child.batch.Cwd, "native", "done", ""),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(sibling.Cwd, "sibling", journalMutation{Op: "add", Kind: "task", Title: new("Sibling work"), State: new("blocked"), Reason: new("sibling-owned")})
	apply(child.batch.Cwd, "native", journalMutation{Op: "add", Kind: "task", Title: new("Native work"), State: new("blocked"), Reason: new("native-owned")})
	apply(child.batch.Cwd, "child",
		journalMutation{Op: "set", P: "/1/1", State: new("done")},
		journalMutation{Op: "set", P: "/1", State: new("done")},
		journalMutation{Op: "add", Kind: "task", Title: new("Native result"), Agent: "/root/worker", State: new("done")})
	u.proxy.journals = newJournalStore()
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"resolved","status":"inProgress"}}}`)
	workflowCompleted(t, u, "child", "resolved", "resolved acceptance")
	workflowCheck(t, u, w, "printf verified", `{"exitCode":0,"stdout":"verified","stderr":""}`)
	workflowReview(t, u, w, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, w, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	if notice := workflowNotice(t, u, w); child.batch.Workflow.Phase != orchestrate.PhaseReady || !strings.Contains(notice, "resolved acceptance") {
		t.Fatal("resolved or unrelated blockers prevented readiness", notice)
	}
}

func TestOrchestrateWorkflowBlockedCorrectionReport(t *testing.T) {
	u, w := workflowUI(t, "check")
	workflowCompleted(t, u, "child", "implement", "initial report")
	workflowCheck(t, u, w, "check", `{"exitCode":1,"stdout":"failed","stderr":""}`)
	correction, _ := workflowTake(t, u, w, "turn/start", "child", orchestrate.DispatchCorrection)
	orchestrateTestReply(t, u, correction, `{"turn":{"id":"correction"}}`)
	child := u.orchestrateThreads["child"]
	if _, err := u.proxy.journals.apply(u.ctx, u.proxy.replayStore, child.batch.Cwd, "child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Production requirement"), State: new("blocked"), Reason: new("net-negative requirement unmet")}}); err != nil {
		t.Fatal(err)
	}
	workflowCompleted(t, u, "child", "correction", "needs a scope decision")
	if notice := workflowNotice(t, u, w); child.batch.Workflow.Phase != orchestrate.PhaseNeedsDecision || !strings.Contains(notice, "/1: net-negative requirement unmet") || !strings.Contains(notice, `Implementer report:\nneeds a scope decision`) {
		t.Fatal("correction lost its blocker or report", notice)
	}
	for _, dispatch := range child.batch.Workflow.Dispatches {
		if dispatch.Kind == orchestrate.DispatchCorrection && dispatch.HostStatus != "completed" {
			t.Fatal("blocked correction lost host completion", dispatch)
		}
	}
}

func TestOrchestrateWorkflowUnavailableJournalAdmission(t *testing.T) {
	for _, state := range []string{"missing", "corrupt", "wrong-run", "conflicted"} {
		t.Run(state, func(t *testing.T) {
			u, w := workflowUI(t)
			child := u.orchestrateThreads["child"]
			path := filepath.Join(u.proxy.replayStore.directory, journalFilename(child.batch.Cwd, "child"))
			var err error
			switch state {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0600)
			default:
				err = u.proxy.journals.transaction(u.ctx, u.proxy.replayStore, child.batch.Cwd, "child", func(j *threadJournal, _ bool) error {
					if state == "wrong-run" {
						j.Orchestration.Main = "other-main"
					} else {
						j.IdentityConflicted = true
					}
					return nil
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			workflowCompleted(t, u, "child", "implement", "report survives")
			if notice := workflowNotice(t, u, w); child.batch.Workflow.Phase != orchestrate.PhaseNeedsDecision || !strings.Contains(notice, "candidate admission:") || !strings.Contains(notice, "report survives") {
				t.Fatal("unavailable journal certified admission or lost report", notice)
			}
		})
	}
}

func TestOrchestrateWorkflowFollowupResumesStoppedWorkflow(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	workflowReviewThread(t, u, w, orchestrate.ReviewCorrectness, "reviewer")
	cancel := workflowCancel(t, u)
	interrupt, _ := workflowTake(t, u, w, "turn/interrupt", "reviewer", "")
	orchestrateTestReply(t, u, interrupt, `{}`)
	if notice := workflowNotice(t, u, w); !strings.Contains(notice, "Workflow canceled") || !strings.Contains(notice, "Send a follow-up to continue the workflow.") {
		t.Fatal("cancel notice is not actionable", notice)
	}
	if result := <-cancel.reply; result.err != nil {
		t.Fatal(result.err)
	}
	follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "follow", input: orchestrateSpawnInput{Message: "continue with the narrowed scope"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(follow)
	drainOrchestrateWork(t, u)
	start, _ := workflowTake(t, u, w, "turn/start", "child", "")
	orchestrateTestReply(t, u, start, `{"turn":{"id":"resumed"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"resumed","status":"inProgress"}}}`)
	child := u.orchestrateThreads["child"]
	if wf := child.batch.Workflow; wf.Phase != orchestrate.PhaseImplementation || wf.CorrectionRounds != 0 || wf.Outcome != nil || child.workflowStopped || wf.Assignment.Scope != "bounded implementation" {
		t.Fatal("follow-up did not resume the workflow", wf)
	}
	// Late host responses from the canceled round never run in the new round.
	late, _ := workflowTake(t, u, w, "thread/start", "", orchestrate.DispatchSimplification)
	raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "late", "cwd": child.batch.Cwd}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"})
	orchestrateTestReply(t, u, late, string(raw))
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"reviewer","turn":{"id":"reviewer-turn","status":"interrupted"}}}`)
	if w.Len() != 0 || child.batch.Workflow.Phase != orchestrate.PhaseImplementation || len(child.workflowWork) != 0 {
		t.Fatal("canceled round affected the resumed round", w.String(), child.batch.Workflow)
	}
	workflowCompleted(t, u, "child", "resumed", "resumed work")
	workflowReview(t, u, w, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, w, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	if notice := workflowNotice(t, u, w); child.batch.Workflow.Phase != orchestrate.PhaseReady || !strings.Contains(notice, "resumed work") {
		t.Fatal("resumed round did not reach readiness", notice)
	}
}

func TestOrchestrateWorkflowReviewWaiver(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("check=%v", required), func(t *testing.T) {
			var checks []string
			if required {
				checks = []string{"printf check"}
			}
			u, w := workflowUI(t, checks...)
			cancel := workflowCancel(t, u)
			interrupt, _ := workflowTake(t, u, w, "turn/interrupt", "child", "")
			orchestrateTestReply(t, u, interrupt, `{}`)
			orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"implement","status":"interrupted"}}}`)
			workflowNotice(t, u, w)
			if result := <-cancel.reply; result.err != nil {
				t.Fatal(result.err)
			}
			follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "waive", reviewWaiver: "User requested correction-only delivery", input: orchestrateSpawnInput{Message: "finish corrections"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(follow)
			drainOrchestrateWork(t, u)
			start, _ := workflowTake(t, u, w, "turn/start", "child", "")
			orchestrateTestReply(t, u, start, `{"turn":{"id":"corrected"}}`)
			orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"corrected","status":"inProgress"}}}`)
			workflowCompleted(t, u, "child", "corrected", "corrections complete")
			if required {
				appServerTestNotify(t, u, "command/exec/outputDelta", map[string]any{"processId": u.orchestrateThreads["child"].workflowWork[workflowValidation].dispatch.ID, "stream": "stdout", "deltaBase64": base64.StdEncoding.EncodeToString([]byte("checked"))})
				workflowCheck(t, u, w, "printf check", `{"exitCode":0,"stdout":""}`)
			}
			if notice := workflowNotice(t, u, w); !strings.Contains(notice, "Further reviews waived") || !strings.Contains(notice, follow.reviewWaiver) || !strings.Contains(notice, "corrections complete") {
				t.Fatal("waived readiness lost its authority or report", notice)
			}
			workflow := u.orchestrateThreads["child"].batch.Workflow
			if workflowDispatched(workflow, orchestrate.DispatchCorrectness) || workflowDispatched(workflow, orchestrate.DispatchSimplification) || w.Len() != 0 {
				t.Fatal("correction-only completion scheduled a review", w.String())
			}
		})
	}
}

func TestUISnapshotOrchestrateWorkflowPhase(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	workflowReviewThread(t, u, w, orchestrate.ReviewCorrectness, "reviewer-c")
	workflowReviewThread(t, u, w, orchestrate.ReviewSimplification, "reviewer-s")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, v := range u.navigation.views {
		v.clock = func() time.Time { return now }
		v.view.clock, v.agents.clock = v.clock, v.clock
		v.view.painter.Theme, v.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
		v.modelsLoading = true
	}
	for _, thread := range []string{"reviewer-c", "reviewer-s"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": thread + "-turn"}})
	}
	u.orchestrationRoster()
	u.shell.focus = 3
	var frame bytes.Buffer
	if err := u.paint(&frame, 100, 24); err != nil {
		t.Fatal(err)
	}
	screen := vt.NewEmulator(100, 24)
	defer screen.Close()
	_, _ = screen.Write(frame.Bytes())
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-workflow-review.txt", screen.String()+"\n")
}

func TestOrchestrateWorkflowRecoveryReconcilesConcurrentReviews(t *testing.T) {
	u, w := workflowUI(t)
	workflowActiveReviews(t, u, w)
	workflowRestart(t, u)
	child := u.orchestrateThreads["child"]
	workflowThreadRead(t, u, w, "reviewer-c", "completed", workflowVerdict(child, orchestrate.ReviewCorrectness))
	workflowThreadRead(t, u, w, "reviewer-s", "inProgress", "")
	if len(child.batch.Workflow.Reviews) != 1 || child.batch.Workflow.Reviews[0].Purpose != orchestrate.ReviewCorrectness {
		t.Fatal("recovery lost host review")
	}
	if w.Len() != 0 || !child.workflowDeferred {
		t.Fatal("startup dispatched instead of retaining phase", w.String())
	}
	// Explicit resume keeps the known active review instead of rereading or relaunching it.
	n := u.ensureOrchestrationNavigation()
	n.resumes = map[string]*orchestrateResume{"child": {}}
	n.finishResume("child", nil)
	drainOrchestrateWork(t, u)
	if w.Len() != 0 {
		t.Fatal("resume repeated recovered host work", w.String())
	}
	workflowCompleted(t, u, "reviewer-s", "reviewer-s-turn", workflowVerdict(child, orchestrate.ReviewSimplification))
	if child.batch.Workflow.Phase != orchestrate.PhaseReady {
		t.Fatal("recovered review did not complete the workflow", child.batch.Workflow)
	}
	workflowNotice(t, u, w)
}

func TestOrchestrateWorkflowUnknownDispatchBlocks(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	w.Reset()
	child := u.orchestrateThreads["child"]
	child.workflowWork = nil
	u.orchestrateRequests = nil
	u.reconcileWorkflow(child)
	drainOrchestrateWork(t, u)
	if child.batch.Workflow.Phase != orchestrate.PhaseNeedsDecision {
		t.Fatal("unknown dispatch did not block")
	}
	workflowNotice(t, u, w)
	u.reconcileWorkflow(child)
	drainOrchestrateWork(t, u)
	if w.Len() != 0 {
		t.Fatal("recovery repeated an uncertain dispatch or terminal notification", w.String())
	}
}

func TestOrchestrateWorkflowManualInputAndCancellation(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	workflowReviewThread(t, u, w, orchestrate.ReviewCorrectness, "reviewer")
	child := u.orchestrateThreads["child"]
	follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", input: orchestrateSpawnInput{Message: "race"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(follow)
	if result := <-follow.reply; result.err == nil {
		t.Fatal("manual followup raced a review")
	}
	cancel := workflowCancel(t, u)
	interrupt, _ := workflowTake(t, u, w, "turn/interrupt", "reviewer", "")
	// A host interrupt acknowledgement is not terminal completion.
	orchestrateTestReply(t, u, interrupt, `{}`)
	if child.batch.Workflow.Phase != orchestrate.PhaseCanceled || !workflowUnsettled(child.batch) {
		t.Fatal("cancel lost active reviewer protection")
	}
	workflowNotice(t, u, w)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"reviewer","turn":{"id":"reviewer-turn","status":"interrupted"}}}`)
	late, _ := workflowTake(t, u, w, "thread/start", "", orchestrate.DispatchSimplification)
	raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "late", "cwd": child.batch.Cwd}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"})
	orchestrateTestReply(t, u, late, string(raw))
	if len(child.workflowWork) != 0 || workflowUnsettled(child.batch) || w.Len() != 0 {
		t.Fatal("interrupt completion revived work or left active reviewer", w.String())
	}
	if result := <-cancel.reply; result.err != nil {
		t.Fatal(result.err)
	}
}

func TestOrchestrateWorkflowRejectsMalformedReviewAndPermissionMismatch(t *testing.T) {
	for _, mode := range []string{"permissions", "cwd", "malformed", "wrong-revision", "wrong-purpose", "clear-reason", "findings-reason"} {
		t.Run(mode, func(t *testing.T) {
			u, w := workflowUI(t)
			workflowCompleted(t, u, "child", "implement", "")
			start, params := workflowTake(t, u, w, "thread/start", "", orchestrate.DispatchCorrectness)
			child := u.orchestrateThreads["child"]
			profile := params["permissions"]
			if mode == "permissions" {
				profile = "different-profile"
			}
			cwd := child.batch.Cwd
			if mode == "cwd" {
				cwd = t.TempDir()
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "reviewer", "cwd": cwd}})
			}
			raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "reviewer", "cwd": cwd}, "activePermissionProfile": map[string]any{"id": profile}, "approvalPolicy": "never"})
			orchestrateTestReply(t, u, start, string(raw))
			if mode == "cwd" {
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "reviewer", "cwd": cwd}})
				if u.navigation.views["reviewer"] != nil || len(u.navigation.pending) != 0 {
					t.Fatal("rejected reviewer metadata admitted a workspace or stranded events")
				}
			}
			if mode != "permissions" && mode != "cwd" {
				turn, _ := workflowTake(t, u, w, "turn/start", "reviewer", "")
				orchestrateTestReply(t, u, turn, `{"turn":{"id":"review"}}`)
				text := "Journal\n\n**This turn**\nCompleted source review without findings"
				if mode != "malformed" {
					result := orchestrate.ReviewResult{Revision: child.batch.Workflow.Candidate.Revision, Purpose: orchestrate.ReviewCorrectness, Verdict: orchestrate.VerdictClear, Findings: []orchestrate.ReviewFinding{}}
					switch mode {
					case "wrong-revision":
						result.Revision = strings.Repeat("a", 40)
					case "wrong-purpose":
						result.Purpose = orchestrate.ReviewSimplification
					case "clear-reason", "findings-reason":
						result.Reason = "Source review complete. Runtime validation remains unverified."
						if mode == "findings-reason" {
							result.Verdict = orchestrate.VerdictFindings
							result.Findings = []orchestrate.ReviewFinding{{ID: "fixture-path", Detail: "Retained fixtures use the old manifest path"}}
						}
					}
					raw, _ := json.Marshal(result)
					text = string(raw)
				}
				workflowCompleted(t, u, "reviewer", "review", text)
			}
			if !workflowTerminal(child.batch.Workflow) || len(child.batch.Workflow.Reviews) != 0 {
				t.Fatal("invalid review counted as approval")
			}
			workflowNotice(t, u, w)
		})
	}
}

func TestOrchestrateWorkflowCancelBeforeReviewThreadAcknowledgement(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	starts := map[orchestrate.WorkflowDispatchKind]btwTestRPC{}
	for _, kind := range []orchestrate.WorkflowDispatchKind{orchestrate.DispatchCorrectness, orchestrate.DispatchSimplification} {
		starts[kind], _ = workflowTake(t, u, w, "thread/start", "", kind)
	}
	child := u.orchestrateThreads["child"]
	workflowCancel(t, u)
	workflowNotice(t, u, w)
	for kind, start := range starts {
		raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "late-" + string(kind), "cwd": child.batch.Cwd}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"})
		orchestrateTestReply(t, u, start, string(raw))
	}
	if w.Len() != 0 || child.batch.Workflow.Phase != orchestrate.PhaseCanceled || workflowUnsettled(child.batch) {
		t.Fatal("late thread ack dispatched work or lost canceled state", w.String())
	}
	for _, d := range child.batch.Workflow.Dispatches {
		if d.ThreadID != "late-"+string(d.Kind) || d.State != orchestrate.DispatchFailed {
			t.Fatal("late thread identity was not retained", d)
		}
	}
}

func TestOrchestrateWorkflowCancelBeforeDispatchIntentAcknowledgement(t *testing.T) {
	u, w := workflowUI(t)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"implement","status":"completed"}}}`)
	// Run ObserveLaunch and candidate admission, leaving BeginWorkflowDispatch pending.
	for i := 0; i < 2; i++ {
		u.completeOrchestrateWork(<-u.orchestrateCompletions)
	}
	if len(u.orchestrateThreads["child"].workflowWork) == 0 {
		t.Fatal("missing pending dispatch")
	}
	c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "interrupt_agent", target: "batch", reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	drainOrchestrateWork(t, u)
	if strings.Contains(w.String(), `"method":"thread/start"`) || strings.Contains(w.String(), `"method":"config/read"`) {
		t.Fatalf("review launch after cancellation admission: %s", w.String())
	}
	if workflowUnsettled(u.orchestrateThreads["child"].batch) {
		t.Fatal("prevented dispatch remains unsettled")
	}
}

func TestOrchestrateWorkflowCancelBeforeInitialThreadAck(t *testing.T) {
	u, w := workflowUI(t)
	_, err := u.proxy.orchestration.store.Prepare(t.Context(), u.session.cwd, "main", "second")
	if err != nil {
		t.Fatal(err)
	}
	spawn := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "spawn_agent", input: orchestrateSpawnInput{TaskName: "second", Message: "bounded"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(spawn)
	drainOrchestrateWork(t, u)
	start := btwTestRequest(t, w, "thread/start", "")
	child := u.orchestrateRequests[fmt.Sprint(start.ID)].child
	cancel := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "interrupt_agent", target: "second", reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(cancel)
	drainOrchestrateWork(t, u)
	notification := btwTestRequest(t, w, "thread/inject_items", "main")
	orchestrateTestReply(t, u, notification, `{}`)
	raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "second-child", "cwd": child.batch.Cwd}})
	orchestrateTestReply(t, u, start, string(raw))
	if strings.Contains(w.String(), `"method":"turn/start"`) {
		t.Fatalf("implementation launched after cancellation: %s", w.String())
	}
}

func TestOrchestrateWorkflowRecoveredActiveReviewersExplicitCancel(t *testing.T) {
	u, w := workflowUI(t)
	workflowActiveReviews(t, u, w)
	workflowRestart(t, u)
	workflowThreadRead(t, u, w, "reviewer-c", "inProgress", "")
	workflowThreadRead(t, u, w, "reviewer-s", "inProgress", "")
	if w.Len() != 0 {
		t.Fatal("discovery dispatched host work", w.String())
	}
	workflowCancel(t, u)
	for _, thread := range []string{"reviewer-c", "reviewer-s"} {
		interrupt, _ := workflowTake(t, u, w, "turn/interrupt", thread, "")
		orchestrateTestReply(t, u, interrupt, `{}`)
	}
	for _, thread := range []string{"reviewer-c", "reviewer-s"} {
		workflowCompleted(t, u, thread, thread+"-turn", "")
	}
	if workflowUnsettled(u.orchestrateThreads["child"].batch) {
		t.Fatal("host completion did not settle canceled reviewers")
	}
}

func TestOrchestrateWorkflowConfiguredReviewer(t *testing.T) {
	u, w := workflowUI(t)
	u.serviceTier = "priority"
	u.proxy.orchestration.reviewers = loadWorkflowReviewerTestConfig(t, "[orchestration.reviewers]\ncorrectness = 'custom-audit'\nsimplification = 'custom-reduce'\n")
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"implement","status":"completed"}}}`)
	drainOrchestrateWork(t, u)
	for _, review := range []struct {
		role string
		kind orchestrate.WorkflowDispatchKind
	}{{"custom-audit", orchestrate.DispatchCorrectness}, {"custom-reduce", orchestrate.DispatchSimplification}} {
		config, _ := workflowTake(t, u, w, "config/read", "", review.kind)
		role := filepath.Join(t.TempDir(), "role.toml")
		if err := os.WriteFile(role, []byte("developer_instructions = ''\nmodel = 'configured-model'\nmodel_reasoning_effort = 'low'\nservice_tier = 'flex'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"layers": []any{map[string]any{"name": map[string]any{"type": "sessionFlags"}, "config": map[string]any{"agents": map[string]any{review.role: map[string]any{"config_file": role, "description": "Configured review"}}}}}})
		orchestrateTestReply(t, u, config, string(raw))
		_, p := workflowTake(t, u, w, "thread/start", "", review.kind)
		if p["developerInstructions"] != "" || p["model"] != "configured-model" || p["serviceTier"] != "flex" || p["config"].(map[string]any)["model_reasoning_effort"] != "low" || p["approvalPolicy"] != "never" || p["sandbox"] != nil || p["permissions"] == nil {
			t.Fatal("configured settings or policy lost", p)
		}
	}
}

func TestOrchestrateWorkflowCancelPendingRoleConfig(t *testing.T) {
	u, w := workflowUI(t)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"implement","status":"completed"}}}`)
	drainOrchestrateWork(t, u)
	var reads []btwTestRPC
	for _, kind := range []orchestrate.WorkflowDispatchKind{orchestrate.DispatchCorrectness, orchestrate.DispatchSimplification} {
		read, _ := workflowTake(t, u, w, "config/read", "", kind)
		reads = append(reads, read)
	}
	workflowCancel(t, u)
	workflowNotice(t, u, w)
	for _, read := range reads {
		orchestrateTestReply(t, u, read, `{"layers":[]}`)
	}
	if w.Len() != 0 || workflowUnsettled(u.orchestrateThreads["child"].batch) {
		t.Fatal("canceled configuration callback dispatched or left uncertain work", w.String())
	}
}

func TestOrchestrateWorkflowMissingSelectedReviewer(t *testing.T) {
	u, w := workflowUI(t)
	u.proxy.orchestration.reviewers = workflowReviewerRoles{Correctness: "not-installed"}
	workflowCompleted(t, u, "child", "implement", "")
	config, _ := workflowTake(t, u, w, "config/read", "", orchestrate.DispatchCorrectness)
	orchestrateTestReply(t, u, config, `{"layers":[]}`)
	child := u.orchestrateThreads["child"]
	if child.batch.Workflow.Phase != orchestrate.PhaseFailed || child.batch.Workflow.Reason == "" {
		t.Fatal("missing selected reviewer did not block visibly", child.batch.Workflow)
	}
	workflowNotice(t, u, w)
}
