package router

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	vt "github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestOrchestrateWorkflowReviewerVisibility(t *testing.T) {
	u, w := workflowUI(t)
	u.proxy.usage = newThreadUsage()
	capture, err := capturer.New(capturer.Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { capture.Close() })
	u.sessionCapture = capture
	handler := capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"review-response","status":"completed","output":[]}`))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","input":[]}`)))
	workflowCompleted(t, u, "child", "implement", "")
	threads := []string{"019eaabb-correctness", "019eaabb-simplification"}
	purposes := []orchestrate.ReviewPurpose{orchestrate.ReviewCorrectness, orchestrate.ReviewSimplification}
	for i, thread := range threads {
		workflowReviewThread(t, u, w, purposes[i], thread)
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "cwd": u.orchestrateThreads["child"].batch.Cwd}})
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": thread + "-turn"}})
		u.proxy.usage.observation(thread, thread, "gpt-6-luna", "").observe(tokenCounts{InputTokens: 1200, UncachedInputTokens: 1200, OutputTokens: 30})
		for range 2 {
			appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": thread, "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 9999}, "last": map[string]any{"totalTokens": 1500}, "modelContextWindow": 4000}})
		}
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": thread + "-turn", "item": map[string]any{"id": "read", "type": "commandExecution", "command": "cat candidate.go"}})
		v := u.navigation.views[thread]
		v.modelsLoading = true
		a := v.session.agent("/root")
		if a.Turns != 1 || !a.Responding || a.ContextTokens != 1500 || a.InputTokens != 1200 || a.OutputTokens != 30 || a.Roundtrips != 1 || !a.CostKnown || a.UsagePartial {
			t.Fatalf("review observation not applied exactly once: %+v", a)
		}
		if len(v.view.entries) == 0 || !v.orchestrateCheckoutUnavailable() {
			t.Fatal("review activity or runtime input ownership lost")
		}
	}
	u.orchestrationRoster()
	if len(u.navigation.pending) != 0 || len(u.navigation.reads) != 0 || len(u.agents.orchestration) != 4 || len(u.agents.orchestrationSources) != 4 {
		t.Fatal("reviewer identities did not resolve into unique roots", u.navigation.pending, u.navigation.reads, u.agents.orchestration)
	}
	var verdicts []string
	for i, thread := range threads {
		name := "/Orchestration/" + u.navigation.views[thread].workflowReview.name
		if !u.pickOrchestratedThread(name) || u.viewedUI().thread != thread {
			t.Fatal("review row did not navigate to its host thread", name)
		}
		v := u.viewedUI()
		v.shell.focus = 0
		appServerTestKeys(t, v, "/session\r")
		sessionMetricField(t, v, "Requests", "Logical", "1")
		appServerTestKeys(t, v, "\x1b")
		before := w.String()
		appServerTestKeys(t, v, "/compact\r")
		if w.String() != before || len(v.unsent)+len(v.queued) != 0 || v.compaction.interrupting {
			t.Fatal("review compaction queued work or interrupted runtime review", w.String())
		}
		verdict := workflowVerdict(u.orchestrateThreads["child"], purposes[i])
		verdicts = append(verdicts, verdict)
		workflowCompleted(t, u, thread, thread+"-turn", verdict)
	}
	var notice struct {
		Items []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(workflowNotice(t, u, w)), &notice); err != nil || len(notice.Items) == 0 {
		t.Fatal("missing Main-bound outcome", err)
	}
	for _, item := range notice.Items {
		for _, content := range item.Content {
			for _, verdict := range verdicts {
				if strings.Contains(content.Text, verdict) {
					t.Fatal("private review verdict escaped to Main", content.Text)
				}
			}
		}
	}
	if u.orchestrateBusy() {
		t.Fatal("completed reviews left orchestration busy", w.String())
	}
}

func TestOrchestrateWorkflowReviewerRestoredUsage(t *testing.T) {
	u, w := workflowUI(t)
	store := usageStoreFixture(t, t.TempDir())
	u.proxy.usage = storedUsageFixture(store)
	workflowActiveReviews(t, u, w)
	u.proxy.usage.observation("reviewer-c", "reviewer-c", "gpt-6-luna", "").observe(tokenCounts{InputTokens: 1200, UncachedInputTokens: 1200, OutputTokens: 30})
	for _, purpose := range []orchestrate.ReviewPurpose{orchestrate.ReviewCorrectness, orchestrate.ReviewSimplification} {
		thread := "reviewer-c"
		if purpose == orchestrate.ReviewSimplification {
			thread = "reviewer-s"
		}
		workflowCompleted(t, u, thread, thread+"-turn", workflowVerdict(u.orchestrateThreads["child"], purpose))
	}
	workflowNotice(t, u, w)
	u.proxy.usage.close()
	u.proxy.usage = storedUsageFixture(store)
	t.Cleanup(u.proxy.usage.close)
	u.closeOrchestratedViews()
	u.navigation = nil
	u.orchestrateThreads = nil
	batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	// Checkout cleanup does not remove confirmed reviewer identity or usage.
	batches[0].State = "removed"
	u.recoverOrchestratedWorkflows(batches)
	drainOrchestrateWork(t, u)
	v := u.navigation.views["reviewer-c"]
	a := v.session.agent("/root")
	if a.InputTokens != 1200 || a.Roundtrips != 1 || !a.UsagePartial || a.Responding || !a.WorkTimer.Since.IsZero() || w.Len() != 0 {
		t.Fatalf("restoration lost usage or revived work: %+v, %s", a, w.String())
	}
	v.modelsLoading = true
	if !u.switchOrchestratedThread("reviewer-c") {
		t.Fatal("retained reviewer not selectable")
	}
	read, _ := workflowTake(t, u, w, "thread/read", "reviewer-c", "")
	raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": "reviewer-c", "cwd": v.session.cwd, "turns": []any{map[string]any{"id": "reviewer-c-turn", "status": "inProgress", "items": []any{map[string]any{"id": "result", "type": "agentMessage", "text": "private retained verdict"}}}}}})
	orchestrateTestReply(t, u, read, string(raw))
	if v.turn != "" || v.session.agent("/root").Responding || !v.turnStarted.IsZero() || !v.session.agent("/root").WorkTimer.Since.IsZero() || u.orchestrateBusy() {
		t.Fatal("recorded review history revived live work", v.turn, v.status, v.session.agent("/root"))
	}
	if w.Len() != 0 || v.session.agent("/root").Roundtrips != 1 || !strings.Contains(fmt.Sprint(v.view.entries), "private retained verdict") {
		t.Fatal("history read dispatched work, duplicated usage, or lost the verdict", w.String(), v.view.entries)
	}
}

func TestOrchestrateWorkflowReviewerFailureSource(t *testing.T) {
	u, w := workflowUI(t)
	workflowActiveReviews(t, u, w)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "reviewer-c", "turn": map[string]any{"id": "reviewer-c-turn"}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "reviewer-c", "turn": map[string]any{"id": "reviewer-c-turn", "status": "failed", "error": map[string]any{"message": "provider failed"}}})
	drainOrchestrateWork(t, u)
	v := u.navigation.views["reviewer-c"]
	if v.agents.agentStatus(*v.session.agent("/root")) != '!' {
		t.Fatal("review failure unavailable to the shared roster status owner")
	}
	v.shell.focus = 0
	for _, input := range []string{"replace verdict", "!pwd", "/btw why"} {
		v.loadDraft(composerDraft{})
		v.setNotice("", false)
		before := w.String()
		appServerTestKeys(t, v, input+"\r")
		if w.String() != before || !strings.HasPrefix(v.notice, "Runtime review owns this thread;") {
			t.Fatal("review input lost its runtime ownership restriction", input, v.notice, w.String())
		}
	}
}

func TestUISnapshotOrchestrateValidationProgress(t *testing.T) {
	u, w := workflowUI(t, "make test")
	u.proxy.usage = newThreadUsage()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, v := range u.navigation.views {
		v.clock = func() time.Time { return now }
		v.view.clock, v.agents.clock = v.clock, v.clock
		v.view.painter.Theme, v.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
		v.modelsLoading = true
		v.turnStarted = now
	}
	workflowCompleted(t, u, "child", "implement", "")
	request, params := workflowTake(t, u, w, "command/exec", "", workflowValidation)
	if params["streamStdoutStderr"] != true {
		t.Fatal("validation output is not streamed", params)
	}
	work := u.orchestrateThreads["child"].workflowWork[workflowValidation]
	run := u.navigation.views["child"].session.commands[[3]string{"child", "", work.dispatch.ID}]
	run.entry.native.commandStarted = now.Add(-time.Second)
	for _, output := range []string{"PASS parser\n", "FAIL validation\n"} {
		appServerTestNotify(t, u, "command/exec/outputDelta", map[string]any{"processId": params["processId"], "stream": "stdout", "deltaBase64": base64.StdEncoding.EncodeToString([]byte(output))})
	}
	v := u.navigation.views["child"]
	run.output.Flush()
	v.flushStreamOutput()
	v.shell.focus = 0
	u.switchOrchestratedThread("child")
	var frame bytes.Buffer
	if err := v.paint(&frame, 100, 24); err != nil {
		t.Fatal(err)
	}
	screen := vt.NewEmulator(100, 24)
	defer screen.Close()
	_, _ = screen.Write(frame.Bytes())
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-validation-progress.txt", screen.String()+"\n")
	// Host byte chunks can split a UTF-8 code point.
	for _, chunk := range []struct{ stream, data string }{{"stdout", "\xc3"}, {"stderr", "X\n"}, {"stdout", "\xa9chec\n"}} {
		appServerTestNotify(t, u, "command/exec/outputDelta", map[string]any{"processId": params["processId"], "stream": chunk.stream, "deltaBase64": base64.StdEncoding.EncodeToString([]byte(chunk.data))})
	}
	orchestrateTestReply(t, u, request, `{"exitCode":1,"stdout":"","stderr":""}`)
	if got := u.orchestrateThreads["child"].batch.Workflow.Checks[0]; got.Output != "PASS parser\nFAIL validation\nX\néchec" || got.Outcome != orchestrate.CheckFailed {
		t.Fatal("streamed failed output not retained", got)
	}
	if len(u.navigation.order) != 2 || v.session.agent("/root").Turns != 0 {
		t.Fatal("command validation created model work")
	}
	if report, known := u.proxy.usage.snapshot("child"); known && report.roundtrips != 0 {
		t.Fatal("command validation fabricated provider usage", report)
	}
	for run.output.Pending() > 0 {
		v.flushStreamOutput()
	}
	if len(v.session.commands) != 0 {
		t.Fatalf("validation command display was not retired: commands=%+v, done=%d, pending=%d, dirty=%v", v.session.commands, len(run.done), run.output.Pending(), run.dirty)
	}
	for _, entry := range v.view.entries {
		if entry.CallID == work.dispatch.ID && entry.native.running {
			t.Fatal("completed check remained running")
		}
	}
}

func TestOrchestrateWorkflowRestoredReviewerOutcomes(t *testing.T) {
	for _, outcome := range []string{"dispatch-failed", "completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			u, w := workflowUI(t)
			workflowActiveReviews(t, u, w)
			batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main")
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "dispatch-failed" {
				for i := range batches[0].Workflow.Dispatches {
					d := &batches[0].Workflow.Dispatches[i]
					if d.ThreadID == "reviewer-c" {
						d.State, d.HostStatus, d.TurnID = orchestrate.DispatchFailed, "", ""
					}
				}
			}
			u.closeOrchestratedViews()
			u.navigation, u.orchestrateThreads, u.orchestrateRequests = nil, nil, nil
			w.Reset()
			u.recoverOrchestratedWorkflows(batches)
			drainOrchestrateWork(t, u)
			if outcome != "dispatch-failed" {
				workflowThreadRead(t, u, w, "reviewer-c", outcome, workflowVerdict(u.orchestrateThreads["child"], orchestrate.ReviewCorrectness))
			}
			v := u.navigation.views["reviewer-c"]
			want := "failed"
			if outcome == "completed" {
				want = "completed"
			}
			u.orchestrationRoster()
			u.picker.orchestration = &orchestrateRunPicker{batches: batches}
			u.refreshOrchestratePicker()
			name := "/Orchestration/" + v.workflowReview.name
			if u.agents.orchestrationLabels[name] != want {
				t.Fatal("restored reviewer roster status", u.agents.orchestrationLabels[name], want)
			}
			found := false
			for _, choice := range u.picker.choices {
				if choice.name == v.thread {
					found = strings.HasPrefix(choice.description, want)
				}
			}
			a := v.session.agent("/root")
			if !found || a.Final != (want == "completed") || a.Responding || v.turn != "" || !v.turnStarted.IsZero() || !a.WorkTimer.Since.IsZero() {
				t.Fatal("reconciled reviewer picker or lifecycle", u.picker.choices, a, v.turn)
			}
		})
	}
}
