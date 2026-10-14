package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotOrchestrateWaitCompletion(t *testing.T) {
	for _, outcome := range []string{"event", "timeout", "cancel", "interrupt"} {
		t.Run(outcome, func(t *testing.T) {
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
			now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
			u.clock, u.view.clock, u.agents.clock = func() time.Time { return now }, func() time.Time { return now }, func() time.Time { return now }
			u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
			out := notificationTestOutput(t, u)
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"waiting-turn"}}}`)
			command := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "wait_agent", callID: "wait", waitTurn: "waiting-turn", waitItem: "wait-item", waitTargets: []string{"batch"}, reply: make(chan orchestrateResult, 1)}
			if outcome == "timeout" {
				command.waitTimeout = 1000
			}
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			registered := <-command.reply
			if registered.err != nil {
				t.Fatal(registered.err)
			}
			item := appServerItem{ID: "wait-item", Type: "mcpToolCall", Server: "orchestrate", Tool: "wait_agent", Status: "completed", Arguments: mustMarshalJSON(map[string]any{"targets": []string{"batch"}}), Result: mustMarshalJSON(map[string]any{"structuredContent": registered.wait})}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "waiting-turn", "item": item})
			now = now.Add(time.Second)
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"waiting-turn","status":"completed"}}}`)
			if out.Len() != 0 {
				t.Errorf("suspended wait emitted completion: %q", out.String())
			}
			if u.turn != "" || !u.session.agent("/root").Final || u.session.agent("/root").Responding {
				t.Fatal("presentation changed actual host lifecycle")
			}
			frame := func(view *appServerUI) []string {
				view.ensureShell()
				view.orchestrationRoster()
				rows, _ := view.mainFrame(90, 7, 0)
				return append(rows, view.agents.nativeRoster(90, 4, now, true)...)
			}
			if outcome == "event" {
				u.writeTerminalTitle(now)
				if !strings.Contains(u.notifications.lastTitle, "⠋") {
					t.Error("suspended wait published an idle terminal title", u.notifications.lastTitle)
				}
				out.Reset()
				assertNativeUISnapshot(t, "orchestration-wait-suspended", frame(u))
				// Owned registration survives restart; transcript alone cannot revive it.
				resumed, wire := resumeOrchestrationMain(t, u.proxy.orchestration.store, u.session.cwd)
				wire.Reset() // The fixture has already acknowledged discovery requests.
				resumed.clock, resumed.view.clock, resumed.agents.clock = u.clock, u.clock, u.clock
				resumed.view.painter.Theme, resumed.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
				resumedOut := notificationTestOutput(t, resumed)
				resumed.restoreMainHistory([]appServerHistoryTurn{{ID: "waiting-turn", Status: "completed", Items: []appServerItem{item}}}, nil, nil)
				drainOrchestrateWork(t, resumed)
				assertNativeUISnapshot(t, "orchestration-wait-resumed", frame(resumed))
				if wire.Len() != 0 || resumedOut.Len() != 0 || resumed.turn != "" {
					t.Fatal("restoring wait dispatched or notified", wire.String(), resumedOut.String())
				}
				history, _ := newAppServerTestUI()
				history.agents = newLiveActivityView()
				history.session.start("main", u.session.cwd)
				history.restoreMainHistory([]appServerHistoryTurn{{ID: "waiting-turn", Status: "completed", Items: []appServerItem{item}}}, nil, nil)
				if strings.Contains(history.sessionLabel(now), "Waiting") || history.session.agent("/root").Responding {
					t.Fatal("history without owned wait revived waiting")
				}
			}
			wire := u.client.Input.(*appServerTestInput)
			switch outcome {
			case "event":
				u.publishOrchestratedEvent(u.orchestrateThreads["child"], orchestrateEvent{Task: "batch", Kind: "message", Message: "wake"})
			case "timeout":
				u.flushOrchestratedEvents()
			case "cancel":
				u.startOrchestratedChild(&orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "wait_agent", waitCancel: "wait", reply: make(chan orchestrateResult, 1)})
			case "interrupt":
				if quit, err := u.key(3); quit || err != nil {
					t.Fatal("interrupt did not stop the suspended wait", quit, err)
				}
			}
			drainOrchestrateWork(t, u)
			if outcome == "event" || outcome == "timeout" {
				orchestrateTestReply(t, u, btwTestRequest(t, wire, "turn/start", "main"), `{}`)
			}
			name := outcome
			if name == "interrupt" {
				name = "cancel"
			}
			assertNativeUISnapshot(t, "orchestration-wait-"+name, frame(u))
			if wire.Len() != 0 || out.Len() != 0 {
				t.Fatal("wait result dispatched extra work or completion", wire.String(), out.String())
			}
			u.writeTerminalTitle(now)
			if strings.Contains(u.notifications.lastTitle, "⠋") {
				t.Fatal("settled wait kept the terminal work marker", u.notifications.lastTitle)
			}
			out.Reset()
			if outcome == "event" || outcome == "cancel" {
				restored, _ := resumeOrchestrationMain(t, u.proxy.orchestration.store, u.session.cwd)
				drainOrchestrateWork(t, restored)
				restored.restoreMainHistory([]appServerHistoryTurn{{ID: "waiting-turn", Status: "completed", Items: []appServerItem{item}}}, nil, nil)
				if strings.Contains(restored.sessionLabel(now), "Wait") {
					t.Fatal("settled wait history replaced the current composer status")
				}
			}
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"next-turn"}}}`)
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"next-turn","status":"completed"}}}`)
			if out.String() != "\x1b]9;Agent turn complete\a" {
				t.Fatal("true completion did not notify once", out.String())
			}
			if outcome == "event" {
				assertNativeUISnapshot(t, "orchestration-wait-finished", frame(u))
			}
		})
	}
}
