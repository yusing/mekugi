package launch

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

type interruptedTrafficFixture struct {
	ui           *appServerUI
	target, work string
}

func trafficCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func interruptedTrafficUI(t *testing.T, nested bool) interruptedTrafficFixture {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Date(2026, 10, 27, 20, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	target := "/root/worker"
	if nested {
		target = "/root/coordinator/worker"
	}
	return interruptedTrafficFixture{ui: u, target: target, work: "worker-work"}
}

func (f interruptedTrafficFixture) request(t *testing.T, id string) appserver.AgentCoordination {
	t.Helper()
	return appserver.AgentCoordination{ThreadID: "main", TurnID: "turn", Projection: appserver.CoordinationProjection{Interrupt: &appserver.CoordinationInterruptRequest{
		OperationID: id, Target: appserver.CoordinationTarget{ThreadID: "worker", TaskName: f.target, WorkID: f.work},
	}}}
}

func (f interruptedTrafficFixture) observe(t *testing.T, event appserver.AgentCoordination) {
	t.Helper()
	appServerTestNotify(t, f.ui, "agent/coordination", event)
}

func (f interruptedTrafficFixture) completion() appserver.AgentCoordination {
	target := appserver.CoordinationTarget{ThreadID: "worker", TaskName: f.target, WorkID: f.work}
	facts := appserver.CoordinationProjection{}
	if f.target == "/root/worker" {
		facts.Deliveries = []appserver.CoordinationTerminalDelivery{{DeliveryID: f.work + ":terminal", Source: target, Recipient: "/root", Disposition: "interrupted", Body: "Earlier progress must not replay."}}
	} else {
		// Main may observe a transitive target's terminal status without
		// receiving the final body owned by its immediate parent.
		facts.Wait = &appserver.CoordinationWaitSettlement{WaitID: "transitive-wait", CallerThreadID: "main", CallerWorkID: "main-work", Mode: "all",
			Origin:   appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "turn", ItemID: "exec", CellID: "wait-cell"},
			Selected: []appserver.CoordinationTarget{target}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"},
			Outcomes: []appserver.CoordinationWaitOutcome{{Target: target, Disposition: "interrupted"}},
		}
	}
	return appserver.AgentCoordination{ThreadID: "main", TurnID: "turn", Projection: facts}
}

func (f interruptedTrafficFixture) complete(t *testing.T) {
	t.Helper()
	f.observe(t, f.completion())
}

func TestUISnapshotInterruptedTraffic(t *testing.T) {
	for _, theme := range []struct {
		name  string
		theme diffrender.Theme
	}{{"dark", diffrender.DarkTheme}, {"light", diffrender.LightTheme}} {
		t.Run(theme.name, func(t *testing.T) {
			f := interruptedTrafficUI(t, false)
			f.observe(t, f.request(t, "interrupt"))
			u := f.ui
			view := u.testActivity(u.shell.Transcript)
			view.Clock, view.Painter.Theme, view.Conversation = u.clock, theme.theme, true
			f.complete(t)
			facts := appserver.CoordinationProjection{}
			for _, status := range []string{"failed", "completed"} {
				name := map[string]string{"failed": "failure", "completed": "success"}[status]
				facts.Deliveries = append(facts.Deliveries, appserver.CoordinationTerminalDelivery{DeliveryID: status, Source: appserver.CoordinationTarget{ThreadID: name, TaskName: "/root/" + name, WorkID: name + "-work"}, Recipient: "/root", Disposition: status, Body: "Result evidence."})
			}
			f.observe(t, appserver.AgentCoordination{ThreadID: "main", TurnID: "turn", Projection: facts})
			settleActivity(u.clock().Add(time.Second), view)
			rows := view.testRenderConversation(90).Lines
			text := ansi.Strip(strings.Join(rows, "\n"))
			if strings.Contains(text, "Interrupt requested") || !strings.Contains(text, "Interrupted /root/worker") || strings.Contains(text, "Earlier progress") || strings.Contains(text, "interrupted") || !strings.Contains(text, "failed") || !strings.Contains(text, "finished") {
				t.Fatalf("terminal outcomes did not settle: %s", text)
			}
			// The native function tool has no Go MCP wrapper. Its retained
			// request receipt, not fabricated tool detail, is source evidence.
			if entry := u.attached.source.Entries[0].Source; entry.Native.InterruptID != "interrupt" || entry.Native.NativeWork == nil || entry.Native.NativeWork.ThreadID != "worker" || entry.Native.NativeWork.WorkID != "worker-work" {
				t.Fatal("settlement discarded exact native request evidence")
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/interrupted-traffic-%s.txt", theme.name), append(rows, "plain after traffic"), 90)
		})
	}
}

func TestInterruptedTrafficSettlementOrdering(t *testing.T) {
	for _, mode := range []string{"outcome-first", "non-parent", "restored"} {
		t.Run(mode, func(t *testing.T) {
			f := interruptedTrafficUI(t, mode == "non-parent")
			request := f.request(t, "interrupt")
			if mode == "non-parent" {
				f.observe(t, request)
			}
			f.complete(t)
			if mode == "restored" {
				terminal := f.completion()
				path := writeTestRollout(t, "main", map[string]any{"timestamp": f.ui.clock().Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "agent_coordination", "threadId": terminal.ThreadID, "turnId": terminal.TurnID, "projection": terminal.Projection}})
				clock := f.ui.clock
				f.ui = newAppServerSessionTestUI(t, t.TempDir())
				f.ui.clock = clock
				trafficCheck(t, f.ui.restoreContent(binding.ThreadInfo{ID: "main", Cwd: f.ui.attached.cwd, Path: path}))
			}
			if mode != "non-parent" {
				f.observe(t, request)
			}
			view := f.ui.testActivity(f.ui.shell.Transcript)
			settleActivity(f.ui.clock().Add(time.Second), view)
			text := ansi.Strip(strings.Join(view.testRenderConversation(90).Lines, "\n"))
			if strings.Contains(text, "Interrupt requested") || !strings.Contains(text, "Interrupted "+f.target) {
				t.Fatalf("%s did not settle: %s", mode, text)
			}
		})
	}
}

func TestInterruptedTrafficIgnoresPriorGenerationOutcome(t *testing.T) {
	f := interruptedTrafficUI(t, false)
	f.observe(t, f.request(t, "interrupt"))
	f.complete(t)
	stale := f.completion()
	stale.Projection.Deliveries[0].DeliveryID = "late-earlier-work"
	f.work = "next-work"
	f.observe(t, f.request(t, "new-interrupt"))
	f.observe(t, stale)
	text := func() string {
		for _, entry := range f.ui.attached.source.Entries {
			if entry.Source.Native != nil && entry.Source.Native.InterruptID == "new-interrupt" {
				return entry.Source.Text
			}
		}
		return ""
	}
	if got := text(); got != "Interrupt requested for "+f.target {
		t.Fatalf("old result settled new request: %s", got)
	}
	f.complete(t)
	if got := text(); got != "Interrupted "+f.target {
		t.Fatalf("current result did not settle new request: %s", got)
	}
}

// A native completion checkpoint retains work without inventing another host
// turn or exposing a Go reduction receipt to the presentation layer.
func nativeCompletionCheckpoint(t *testing.T) (*appServerUI, *threadExecution) {
	t.Helper()
	u, threads, _ := nativeRunningUI(t)
	appServerTestNotify(t, u, "turn/checkpointed", appserver.TurnCheckpointed{
		ThreadID: "main", TurnID: "first", WorkID: "main-work",
		Checkpoint: appserver.TurnCheckpoint{Kind: "completion_pending", DeliveryID: "final-delivery"}, Targets: []string{"child"},
	})
	appServerOneRequest(t, u.Host.Input.(*appServerTestInput), "agent/observeTree", "")
	nativeTreeFixture(t, threads)
	return u, threads
}

func TestCompletionGuardSkippedContinuationSettlesRoster(t *testing.T) {
	u, threads := nativeCompletionCheckpoint(t)
	if !u.Busy(false) || u.attached.session.Lifecycle("main").Final {
		t.Fatal("pending descendants published a completed parent")
	}
	threads.native.agent("child").Work.Disposition = "completed"
	threads.native.agent("main").Work.Disposition = "completed"
	nativeTreeFixture(t, threads)
	u.testMainFrame(80, 8, 0)
	if !u.attached.session.Lifecycle("main").Final || u.Busy(false) || !u.attached.presentation.View().Status.Session.CompletionKnown {
		t.Fatal("native terminal work did not settle the roster")
	}
	if wire := u.Host.Input.(*appServerTestInput); wire.Len() != 0 {
		t.Fatalf("settlement dispatched an unnecessary continuation: %s", wire.String())
	}
}

func TestCompletionGuardWaitsForExactReduction(t *testing.T) {
	for _, outcome := range []string{"completed", "continued", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			u, threads := nativeCompletionCheckpoint(t)
			wire := u.Host.Input.(*appServerTestInput)
			// A stale tree for another work generation cannot settle the exact
			// work that emitted this checkpoint.
			stale := threads.native.families["main"].tree
			stale.Agents = append([]appserver.AgentObservation(nil), stale.Agents...)
			stale.Agents[0].Work = &appserver.AgentWorkObservation{WorkID: "earlier-work", Disposition: "completed"}
			raw, err := json.Marshal(struct {
				Tree appserver.AgentTreeObservation `json:"tree"`
			}{stale})
			trafficCheck(t, err)
			trafficCheck(t, threads.native.treeResponse("main", appserver.Message{Result: raw}))
			u.testMainFrame(80, 8, 0)
			if u.NativeWork != "main-work" || u.attached.presentation.View().Status.Session.CompletionKnown || !u.Busy(false) {
				t.Fatal("stale native generation settled pending completion")
			}
			root := threads.native.agent("main")
			root.Work = &appserver.AgentWorkObservation{WorkID: "main-work", Disposition: "running"}
			if outcome == "interrupted" {
				if c := nativeTestCancel(t, u); c.quit {
					t.Fatal("cancellation quit pending work")
				}
				request := appServerOneRequest(t, wire, "agent/interrupt", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
				if !u.Busy(false) {
					t.Fatal("interrupt acknowledgement claimed terminal work")
				}
			}
			if outcome == "continued" {
				nativeTreeFixture(t, threads)
				appServerTestTurn(t, u, "continued")
				if !u.Busy(false) || u.Turn != "continued" || u.nativeCheckpoint != nil {
					t.Fatal("native continuation did not retire the old checkpoint")
				}
				return
			}
			root.Work.Disposition = outcome
			nativeTreeFixture(t, threads)
			u.testMainFrame(80, 8, 0)
			if u.Busy(false) || u.attached.session.Lifecycle("main").Final != (outcome == "completed") {
				t.Fatalf("native %s failed to settle pending work", outcome)
			}
		})
	}
}

func TestCompletionDeferralPreservesExplicitHostOutcomes(t *testing.T) {
	for _, status := range []string{"interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			u, threads, _ := nativeRunningUI(t)
			appServerTestTurnEnd(t, u, "first", status)
			threads.native.agent("main").Work.Disposition = status
			nativeTreeFixture(t, threads)
			if u.Busy(false) || u.attached.session.Lifecycle("main").Final {
				t.Fatal("explicit native failure or interruption became successful completion")
			}
		})
	}
}

func TestFailedCompletionReductionDoesNotPublishSuccess(t *testing.T) {
	u, threads := nativeCompletionCheckpoint(t)
	trafficCheck(t, threads.native.treeResponse("main", appserver.Message{Error: &appserver.Error{Code: -1, Message: "observation unavailable"}}))
	u.testMainFrame(80, 8, 0)
	if !u.Busy(false) || u.attached.session.Lifecycle("main").Final || u.attached.presentation.View().Status.Session.CompletionKnown {
		t.Fatal("failed native observation published successful completion")
	}
	threads.native.agent("main").Work.Disposition = "completed"
	nativeTreeFixture(t, threads)
	u.testMainFrame(80, 8, 0)
	if u.Busy(false) || !u.attached.presentation.View().Status.Session.CompletionKnown {
		t.Fatal("failed observation blocked a later authoritative completion")
	}
}
