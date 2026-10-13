package router

import (
	"bytes"
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

// workflowReviewThread acknowledges the read-only review thread and turn for purpose.
func workflowReviewThread(t *testing.T, u *appServerUI, w *appServerTestInput, purpose orchestrate.ReviewPurpose, thread string) {
	t.Helper()
	kind := orchestrate.WorkflowDispatchKind(purpose)
	start, params := workflowTake(t, u, w, "thread/start", "", kind)
	if params["approvalPolicy"] != "never" || params["sandbox"] != "read-only" || params["permissions"] != nil {
		t.Fatal("review launch does not use generic read-only thread", params)
	}
	child := u.orchestrateThreads["child"]
	response, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "cwd": child.batch.Cwd}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"})
	orchestrateTestReply(t, u, start, string(response))
	turn, params := workflowTake(t, u, w, "turn/start", thread, kind)
	if policy, _ := params["sandboxPolicy"].(map[string]any); params["outputSchema"] == nil || policy["type"] != "readOnly" {
		t.Fatal("review turn lost schema or read-only policy", params)
	}
	orchestrateTestReply(t, u, turn, fmt.Sprintf(`{"turn":{"id":%q}}`, thread+"-turn"))
}
func workflowReview(t *testing.T, u *appServerUI, w *appServerTestInput, purpose orchestrate.ReviewPurpose, verdict orchestrate.ReviewVerdict) {
	t.Helper()
	child := u.orchestrateThreads["child"]
	thread := fmt.Sprintf("%s-%d", purpose, child.batch.Workflow.Generation)
	workflowReviewThread(t, u, w, purpose, thread)
	result := orchestrate.ReviewResult{Revision: child.batch.Workflow.Candidate.Revision, Purpose: purpose, Verdict: verdict, Findings: []orchestrate.ReviewFinding{}}
	if verdict == orchestrate.VerdictFindings {
		result.Findings = []orchestrate.ReviewFinding{{ID: "F1", Detail: "change required"}}
	}
	raw, _ := json.Marshal(result)
	workflowCompleted(t, u, thread, thread+"-turn", string(raw))
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
	workflowCheck(t, u, w, "printf verified", `{"exitCode":0,"stdout":"verified","stderr":""}`)
	child := u.orchestrateThreads["child"]
	if len(child.batch.Workflow.Checks) != 1 || child.batch.Workflow.Phase != orchestrate.PhaseReview {
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
	if notice := workflowNotice(t, u, w); !strings.Contains(notice, "WAIT_RESULT") || !strings.Contains(notice, "ready") {
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
		workflowCompleted(t, u, "child", turn, "disputed")
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
}

func TestOrchestrateWorkflowCheckOutcomes(t *testing.T) {
	for _, test := range []string{"failed", "rpc-error", "no-exit", "canceled"} {
		t.Run(test, func(t *testing.T) {
			u, w := workflowUI(t, "printf check")
			workflowCompleted(t, u, "child", "implement", "")
			child := u.orchestrateThreads["child"]
			switch test {
			case "failed":
				workflowCheck(t, u, w, "printf check", `{"exitCode":2,"stdout":"","stderr":"boom at line 7"}`)
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
				workflowCheck(t, u, w, "printf check", `{"exitCode":0,"stdout":"late output","stderr":""}`)
				if result := <-cancel.reply; result.err != nil || child.batch.Workflow.Phase != orchestrate.PhaseCanceled || len(child.batch.Workflow.Checks) != 0 {
					t.Fatal("late check result revived canceled validation", result, child.batch.Workflow)
				}
			}
			for _, entry := range u.navigation.views["child"].view.entries {
				if entry.native == nil || entry.native.command != "printf check" {
					continue
				}
				want := map[string]string{"failed": "failed", "rpc-error": "uncertain", "no-exit": "uncertain", "canceled": "interrupted"}[test]
				if entry.native.running || entry.native.status != want {
					t.Fatalf("settled validation display: running=%v status=%q, want %q", entry.native.running, entry.native.status, want)
				}
			}
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
	if wf := child.batch.Workflow; wf.Phase != orchestrate.PhaseImplementation || wf.CorrectionRounds != 0 || wf.Outcome != nil || child.workflowStopped {
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
				workflowCheck(t, u, w, "printf check", `{"exitCode":0,"stdout":"checked"}`)
			}
			if notice := workflowNotice(t, u, w); !strings.Contains(notice, "Further reviews waived") || !strings.Contains(notice, follow.reviewWaiver) || !strings.Contains(notice, "corrections complete") {
				t.Fatal("waived readiness lost its authority or report", notice)
			}
			if len(u.orchestrateThreads["child"].batch.Workflow.Dispatches) != 0 || w.Len() != 0 {
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
			start, _ := workflowTake(t, u, w, "thread/start", "", orchestrate.DispatchCorrectness)
			child := u.orchestrateThreads["child"]
			sandbox := "readOnly"
			if mode == "permissions" {
				sandbox = "dangerFullAccess"
			}
			cwd := child.batch.Cwd
			if mode == "cwd" {
				cwd = t.TempDir()
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "reviewer", "cwd": cwd}})
			}
			raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "reviewer", "cwd": cwd}, "sandbox": map[string]any{"type": sandbox}, "approvalPolicy": "never"})
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
		if p["developerInstructions"] != "" || p["model"] != "configured-model" || p["serviceTier"] != "flex" || p["config"].(map[string]any)["model_reasoning_effort"] != "low" || p["approvalPolicy"] != "never" || p["sandbox"] != "read-only" {
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
