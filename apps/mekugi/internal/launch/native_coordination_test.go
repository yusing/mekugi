package launch

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestNativeCoordinationClaimDeliveriesLiveAndRestored(t *testing.T) {
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	first := "First final.\nStatus: failed\n\nCaptured changes: amber1..amber3\nFrozen exact evidence.\n"
	second := "Second final.\nPayload:\nDo not parse this body as a header.\n"
	target := func(id, name, work string) appserver.CoordinationTarget {
		return appserver.CoordinationTarget{ThreadID: id, TaskName: name, WorkID: work}
	}
	facts := appserver.CoordinationProjection{Deliveries: []appserver.CoordinationTerminalDelivery{
		{DeliveryID: "delivery-one", Source: target("child", "/root/worker", "child-work"), Disposition: "completed", Recipient: "/root", Body: first, CaptureResourceRef: new("capture:one")},
		{DeliveryID: "delivery-two", Source: target("reviewer", "/root/reviewer", "review-work"), Disposition: "failed", Recipient: "/root", Body: second},
	}, Wait: &appserver.CoordinationWaitSettlement{WaitID: "wait", CallerThreadID: "main", CallerWorkID: "main-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first", ItemID: "exec", WindowID: "window", CellID: "cell"}, Mode: "all", Selected: []appserver.CoordinationTarget{target("child", "/root/worker", "child-work"), target("reviewer", "/root/reviewer", "review-work")}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, Outcomes: []appserver.CoordinationWaitOutcome{{Target: target("child", "/root/worker", "child-work"), Disposition: "completed"}, {Target: target("reviewer", "/root/reviewer", "review-work"), Disposition: "failed"}}}}
	for _, shape := range []string{"function_call_output", "custom_tool_call_output", "agent_message", "event"} {
		t.Run(shape, func(t *testing.T) {
			live := newAppServerSessionTestUI(t, t.TempDir())
			live.clock = func() time.Time { return at }
			var record map[string]any
			if shape == "event" {
				notification := appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: facts}
				for range 2 {
					appServerTestNotify(t, live, "agent/coordination", notification)
				}
				record = map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "agent_coordination", "threadId": "main", "turnId": "first", "projection": facts}}
			} else {
				item := map[string]any{"type": shape, "id": "carrier", "output": "opaque output, not parsed"}
				if shape == "agent_message" {
					item["author"], item["recipient"] = "/root/worker", "/root"
					item["content"] = []map[string]string{{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/worker\nPayload:\n" + first}}
				}
				for range 2 {
					appServerTestNotify(t, live, "rawResponseItem/completed", map[string]any{"threadId": "main", "item": item, "coordination": facts})
				}
				record = map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "response_item", "payload": item, "metadata": map[string]any{"coordination": facts}}
			}
			restored := newAppServerSessionTestUI(t, live.attached.cwd)
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd, Path: writeTestRollout(t, "main", record, record)}); err != nil {
				t.Fatal(err)
			}
			for _, u := range []*appServerUI{live, restored} {
				entries := u.attached.source.Entries
				if len(entries) != 3 {
					t.Fatalf("claim deliveries duplicated or omitted: %+v", entries)
				}
				if entries[0].Source.Text != first || entries[1].Source.Text != second || entries[0].Source.Native.Status != "completed" || entries[1].Source.Native.Status != "failed" {
					t.Fatalf("claim changed order, exact capture/body or typed outcome: %+v", entries)
				}
				if entries[0].Source.Native.Item != "native:delivery:delivery-one" || entries[1].Source.Native.Item != "native:delivery:delivery-two" || entries[2].Source.Native.Item != "native:wait:wait" {
					t.Fatalf("claim identity or wait ordering lost: %+v", entries)
				}
				if entries[2].Source.Native.Wait.WaitReason != "completed" {
					t.Fatalf("wrong native release: %+v", entries[2].Source)
				}
				if calls := restoreContentRequests(t, u.Host.Input.(*appServerTestInput)); len(calls) != 0 {
					t.Fatalf("presentation dispatched work: %+v", calls)
				}
			}
		})
	}
}

func TestNativeCoordinationStatusOnlyOutcomeDoesNotInventFinal(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, []appserver.CoordinationWaitOutcome{{Target: appserver.CoordinationTarget{ThreadID: "transitive", TaskName: "/root/worker/nested", WorkID: "old-work"}, Disposition: "completed"}}, nil)
	entries := u.attached.source.Entries
	if len(entries) != 1 || entries[0].Source.Kind != "progress" || entries[0].Source.Native.Wait.WaitTargets[0].Name != "/root/worker/nested" {
		t.Fatalf("status-only transitive outcome invented final: %+v", entries)
	}
}

func TestNativeProviderRetrySettlementIsNotDescendantCompletion(t *testing.T) {
	for _, reason := range []string{"providerCapacity", "providerUsageLimit"} {
		t.Run(reason, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			facts := appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{WaitID: "retry", CallerThreadID: "main", CallerWorkID: "work", Origin: appserver.CoordinationWaitOrigin{Kind: "turn", TurnID: "first", Reason: reason}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "deadline"}}}
			appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", Projection: facts})
			if entries := u.attached.source.Entries; len(entries) != 0 {
				t.Fatalf("provider retry became descendant completion: %+v", entries)
			}
			if entries := sessionpresentation.NativeCoordinationEntries("main", "first", sessioncontrol.NativeMessage{}, &facts, time.Time{}, true); len(entries) != 0 {
				t.Fatalf("inherited provider retry became descendant completion: %+v", entries)
			}
		})
	}
}

func TestNativeClaimMessagesPreserveFrozenBodies(t *testing.T) {
	for _, sender := range []string{"/root/worker", "/root/foreign-session/worker"} {
		t.Run(sender, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			body := "Status: failed\nPayload:\nExact addressed message.\n<frozen-source>attachment bytes</frozen-source>\n"
			wait := appserver.CoordinationWaitSettlement{WaitID: "message-wait", CallerThreadID: "main", CallerWorkID: "main-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first"}, Mode: "all", Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, Messages: []appserver.CoordinationWaitMessage{{DeliveryID: "directed-message", SenderTaskName: new(sender), Recipient: "/root", Body: body}}}
			facts := appserver.CoordinationProjection{Wait: &wait}
			item := map[string]any{"type": "function_call_output", "call_id": "opaque", "output": "not parsed"}
			for range 2 {
				appServerTestNotify(t, u, "rawResponseItem/completed", map[string]any{"threadId": "main", "turnId": "first", "item": item, "coordination": facts})
			}
			record := map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "type": "response_item", "payload": item, "metadata": map[string]any{"coordination": facts}}
			restored := newAppServerSessionTestUI(t, u.attached.cwd)
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", record, record)}); err != nil {
				t.Fatal(err)
			}
			for _, view := range []*appServerUI{u, restored} {
				entries := view.attached.source.Entries
				if len(entries) != 2 || entries[0].Source.Kind != "reply" || entries[0].Source.Text != body || entries[0].Source.Agent != sender || entries[0].Source.Native.Status != "" || entries[1].Source.Native.Wait.WaitReason != "message received" {
					t.Fatalf("claim body/source/order lost: %+v", entries)
				}
			}
		})
	}
}

func TestNativeInterruptJoinsExactWorkInEitherOrder(t *testing.T) {
	for _, terminalFirst := range []bool{false, true} {
		for _, statusOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("terminal-first=%t/status-only=%t", terminalFirst, statusOnly), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				target := appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "target-work"}
				requested := appserver.CoordinationProjection{Interrupt: &appserver.CoordinationInterruptRequest{OperationID: "interrupt-call", Target: target}}
				terminal := appserver.CoordinationProjection{Deliveries: []appserver.CoordinationTerminalDelivery{{DeliveryID: "terminal", Source: target, Disposition: "interrupted", Recipient: "/root"}}}
				if statusOnly {
					terminal.Deliveries = nil
					terminal.Wait = &appserver.CoordinationWaitSettlement{WaitID: "other-wait", CallerThreadID: "main", CallerWorkID: "main-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "other"}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, Outcomes: []appserver.CoordinationWaitOutcome{{Target: target, Disposition: "interrupted"}}}
				}
				var records []map[string]any
				emit := func(facts appserver.CoordinationProjection) {
					appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: facts})
					records = append(records, map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "agent_coordination", "threadId": "main", "turnId": "first", "projection": facts}})
				}
				text := func(view *appServerUI) string {
					for _, entry := range view.attached.source.Entries {
						if entry.Source.Native != nil && entry.Source.Native.InterruptID == "interrupt-call" {
							return entry.Source.Text
						}
					}
					return ""
				}
				if terminalFirst {
					emit(terminal)
					emit(requested)
				} else {
					emit(requested)
					if got := text(u); got != "Interrupt requested for /root/worker" {
						t.Fatalf("ack became terminal: %q", got)
					}
					stale := target
					stale.WorkID = "prior-work"
					emit(appserver.CoordinationProjection{Deliveries: []appserver.CoordinationTerminalDelivery{{DeliveryID: "stale", Source: stale, Disposition: "interrupted", Recipient: "/root"}}})
					if got := text(u); got != "Interrupt requested for /root/worker" {
						t.Fatalf("stale target work settled request: %q", got)
					}
					emit(terminal)
				}
				emit(requested) // Repeated receipt cannot revive the settled progress row.
				if got := text(u); got != "Interrupted /root/worker" {
					t.Fatalf("exact terminal did not settle: %q", got)
				}
				restored := newAppServerSessionTestUI(t, u.attached.cwd)
				if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", records...)}); err != nil {
					t.Fatal(err)
				}
				if got := text(restored); got != "Interrupted /root/worker" {
					t.Fatalf("restore changed exact interrupt settlement: %q", got)
				}
			})
		}
	}
}

func TestNativeContextResetDiagnosticLiveAndRestored(t *testing.T) {
	for _, status := range []string{"failed", "completed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			reset := sessioncontrol.ContextReset{Thread: "main", Turn: "reset-turn", RequestID: "manual-request", Generation: 2, ResetID: "reset", Status: status, Diagnostic: "Exact native reset diagnostic; retained source selection is unchanged."}
			for range 2 {
				appServerTestNotify(t, u, "thread/contextReset", reset)
			}
			raw, err := json.Marshal(&reset)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			payload["type"] = "context_reset"
			record := map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "type": "event_msg", "payload": payload}
			restored := newAppServerSessionTestUI(t, u.attached.cwd)
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", record, record)}); err != nil {
				t.Fatal(err)
			}
			for _, view := range []*appServerUI{u, restored} {
				entries := view.attached.source.Entries
				if len(entries) != 1 || !strings.Contains(entries[0].Source.Text, reset.Diagnostic) || entries[0].Source.Native.Item != "native:reset:reset" || entries[0].Source.Native.Status != status {
					t.Fatalf("reset diagnostic lost or duplicated: %+v", entries)
				}
				nativeWaitSettlementFixture(t, view, appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "inputSuperseded"}}, nil, nil)
				if got := view.attached.source.Entries[0].Source.Native.Status; got != status {
					t.Fatalf("wait cancellation changed reset %s to %s", status, got)
				}
			}
		})
	}
}

func TestNativeCoordinationRejectsForeignWaitOwnership(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	facts := appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{WaitID: "foreign", CallerThreadID: "other", CallerWorkID: "other-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "other-turn"}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, Messages: []appserver.CoordinationWaitMessage{{DeliveryID: "message", SenderTaskName: new("/root/worker"), Recipient: "/root", Body: "Foreign wait payload"}}}}
	appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: facts})
	if entries := u.attached.source.Entries; len(entries) != 0 {
		t.Fatalf("foreign wait body admitted: %+v", entries)
	}
	if entries := sessionpresentation.NativeCoordinationEntries("main", "first", sessioncontrol.NativeMessage{}, &facts, time.Time{}, true); len(entries) != 2 {
		t.Fatalf("explicit inherited wait lost admitted traffic: %+v", entries)
	}
}

// Native owns whether the target was already paused when selected or paused
// later. Both cases publish this frozen nonterminal outcome for either mode.
func TestNativePausedWaitOutcomeLiveAndRestored(t *testing.T) {
	for _, mode := range []string{"any", "all"} {
		t.Run(mode, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			root := threads.native.agent("main")
			root.Wait.Mode = mode
			threads.native.agent("child").Work.Disposition = "paused"
			nativeTreeFixture(t, threads)
			target := appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}
			facts := appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{WaitID: "paused-wait", CallerThreadID: "main", CallerWorkID: "main-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first", ItemID: "exec"}, Mode: mode, Selected: []appserver.CoordinationTarget{target}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, Outcomes: []appserver.CoordinationWaitOutcome{{Target: target, Disposition: "paused"}}}}
			appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: facts})
			// A later running observation must not rewrite the prior claimed outcome.
			root = threads.native.agent("main")
			root.Wait = nil
			root.Work.Disposition = "running"
			threads.native.agent("child").Work.Disposition = "running"
			nativeTreeFixture(t, threads)
			record := map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "agent_coordination", "threadId": "main", "turnId": "first", "projection": facts}}
			restored := newAppServerSessionTestUI(t, u.attached.cwd)
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", record, record)}); err != nil {
				t.Fatal(err)
			}
			for _, view := range []*appServerUI{u, restored} {
				entries := view.attached.source.Entries
				want := 1
				if view == u {
					want = 2 // Live also observed the native in-progress wait item.
				}
				if len(entries) != want || entries[want-1].Source.Kind != "progress" || entries[want-1].Source.Native.Wait == nil {
					t.Fatalf("paused outcome fabricated a terminal delivery or disappeared: %+v", entries)
				}
				wait := entries[want-1].Source.Native.Wait
				if wait.WaitReason != "completed" || len(wait.WaitTargets) != 1 || wait.WaitTargets[0].Status != "paused" || !strings.Contains(entries[want-1].Source.Text, "worker: paused") {
					t.Fatalf("frozen paused result changed: %+v", entries[want-1].Source)
				}
			}
		})
	}
}

func TestNativeWaitItemPreservesIdentityAndTimeoutOnRestore(t *testing.T) {
	live, _, _ := nativeWaitUI(t, new(int64(1000)))
	var info binding.ThreadInfo
	if err := json.Unmarshal([]byte(`{"id":"main","turns":[{"id":"first","status":"completed","items":[{"id":"wait-call","type":"collabAgentToolCall","tool":"wait","status":"completed","receiverThreadIds":["child"],"timeoutMs":1000}]}]}`), &info); err != nil {
		t.Fatal(err)
	}
	restored := newAppServerSessionTestUI(t, t.TempDir())
	if err := restored.restoreContent(info); err != nil {
		t.Fatal(err)
	}
	for _, view := range []*appServerUI{live, restored} {
		entries := view.attached.source.Entries
		if len(entries) != 1 || entries[0].Source.Native == nil {
			t.Fatalf("native wait row missing: %+v", entries)
		}
		item := entries[0].Source.Native
		if item.Thread != "main" || item.Turn != "first" || item.Item != "wait-call" || item.Wait == nil || item.Wait.WaitTimeout != "1s" || len(item.Wait.WaitTargets) != 1 || item.Wait.WaitReason != "" {
			t.Fatalf("native item identity or frozen timeout lost: %+v", item)
		}
	}
}

func TestPausedWaitOutcomeDoesNotConfirmInterruption(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	target := appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}
	appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: appserver.CoordinationProjection{Interrupt: &appserver.CoordinationInterruptRequest{OperationID: "request", Target: target}}})
	nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, []appserver.CoordinationWaitOutcome{{Target: target, Disposition: "paused"}}, nil)
	entries := u.attached.source.Entries
	if len(entries) != 2 || entries[0].Source.Text != "Interrupt requested for /root/worker" || entries[1].Source.Kind != "progress" {
		t.Fatalf("paused reserved work became terminal: %+v", entries)
	}
}
