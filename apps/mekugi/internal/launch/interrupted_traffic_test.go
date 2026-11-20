package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
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
			if strings.Contains(text, "Interrupt requested") || !strings.Contains(text, "Interrupted worker") || strings.Contains(text, "Earlier progress") || strings.Contains(text, "interrupted") || !strings.Contains(text, "failed") || !strings.Contains(text, "finished") {
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
			if strings.Contains(text, "Interrupt requested") || !strings.Contains(text, "Interrupted "+pathdisplay.AgentDisplayName(f.target)) {
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
	if got := text(); got != "Interrupt requested for "+pathdisplay.AgentDisplayName(f.target) {
		t.Fatalf("old result settled new request: %s", got)
	}
	f.complete(t)
	if got := text(); got != "Interrupted "+pathdisplay.AgentDisplayName(f.target) {
		t.Fatalf("current result did not settle new request: %s", got)
	}
}

// A native completion checkpoint retains work without inventing another host
// turn or exposing a Go reduction receipt to the presentation layer.
