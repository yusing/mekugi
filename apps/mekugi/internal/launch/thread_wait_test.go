package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"

	"github.com/yusing/mekugi/libraries/diffrender"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestUISnapshotNativeRetryProgress(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u, at := newActiveWorkTestUI(t)
			u.attached.cwd = "/workspace"
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.shell.Agents.Painter.Theme = diffrender.DarkTheme
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
			notify := func(turn, message string, retry bool) {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": retry,
					"error": map[string]any{"message": message, "additionalDetails": "Server overloaded", "codexErrorInfo": "serverOverloaded"}})
			}
			const capacityRetry = "Retrying in 5m 11s (1/5)"
			for _, message := range []string{capacityRetry, "Reconnecting... 2/5"} {
				notify("first", message, true)
				rows, _ := u.testMainFrame(100, 8, 0)
				view := u.attached.presentation.View()
				if thread == "main" && (!strings.Contains(view.Status.Composer.Text, message) || view.Status.Composer.Alert || !view.InterruptAvailable) {
					t.Fatalf("retry lost from shared status: %+v", view.Status)
				}
				if got := u.attached.session.RetryStatus(thread); !strings.Contains(got, message) || !strings.Contains(got, "Server overloaded") {
					t.Fatalf("retry facts lost: %q", got)
				}
				if message == capacityRetry && thread == "main" {
					if frame := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(frame, capacityRetry+" · Server overloaded") {
						t.Fatalf("capacity retry not rendered in the terminal:\n%s", frame)
					}
				}
				if message == "Reconnecting... 2/5" {
					if thread == "main" {
						assertNativeUISnapshot(t, "native-retry-status", rows)
					}
					u.testActivity(u.shell.Agents).publishActivity()
					assertNativeUISnapshot(t, "native-retry-roster-"+thread, u.shell.Agents.RosterSheet(100, 6, *at))
				}
			}
			notify("old", "stale retry", true)
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "old", "status": "completed"}})
			if !strings.Contains(u.attached.session.RetryStatus(thread), "2/5") {
				t.Fatal("stale turn changed current retry")
			}
			appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": "first", "itemId": "answer", "delta": "Recovered"})
			if u.attached.session.RetryStatus(thread) != "" {
				t.Fatal("response did not clear retry")
			}
			for _, outcome := range []string{"completed", "interrupted", "failed"} {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": outcome}})
				notify(outcome, "Reconnecting... 1/5", true)
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": outcome, "status": outcome}})
				notify(outcome, "late retry", true)
				if u.attached.session.RetryStatus(thread) != "" {
					t.Fatalf("%s retained or reopened retry", outcome)
				}
			}
			notify("first", "terminal error", false)
			if u.attached.session.RetryStatus(thread) != "" {
				t.Fatal("terminal error became retry progress")
			}
		})
	}
}

func TestNativeProviderFailureDiagnosticKeepsOriginalWork(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u, _, _ := nativeRunningUI(t)
			if thread == "child" {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
			}
			notify := func(turn, message string, retry bool) {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": retry, "error": map[string]any{"message": message, "additionalDetails": "Exact provider diagnostic"}})
			}
			notify("old", "Stale failure", false)
			notify("first", "Reconnecting... 1/5", true)
			notify("first", "Reconnecting... 2/5", true)
			for _, entry := range u.attached.childSource.Entries {
				if entry.Source.Kind == "error" {
					t.Fatal("stale failure or retry became a diagnostic row")
				}
			}
			for range 2 {
				notify("first", "Provider stream failed", false)
			}
			count := 0
			for _, entry := range u.attached.childSource.Entries {
				if entry.Source.Kind == "error" {
					count++
					if entry.Source.ErrorDetail != "Provider stream failed · Exact provider diagnostic" || entry.Source.Native.Turn != "first" {
						t.Fatalf("provider evidence changed: %+v", entry)
					}
				}
			}
			if count != 1 || u.Turn != "first" || u.attached.session.RetryStatus(thread) != "" || u.attached.session.Lifecycle(thread).Final {
				t.Fatalf("provider diagnostic duplicated, retried or completed original work: count=%d turn=%s", count, u.Turn)
			}
			view := u.testActivity(u.shell.Agents)
			if thread == "main" {
				view = u.testActivity(u.shell.Transcript)
			}
			paint := ansi.Strip(strings.Join(view.testRenderConversation(120).Lines, "\n"))
			if !strings.Contains(paint, "Provider stream failed") {
				t.Fatalf("provider failure absent from rendered consumer: %s", paint)
			}
		})
	}
}

func TestRecoveryInputRetriesWithoutAnotherHostMessage(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.NativeInputPending = true
	u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "queued during recovery"}}
	if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || len(u.attached.conversation.Unsent) != 1 {
		t.Fatal("input bypassed native admission")
	}
	u.NativeInputPending = false
	if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, wire, "turn/start", "queued during recovery")
}

func TestLogicalWaitPublicationUsesNativeIdentity(t *testing.T) {
	u, _, _ := nativeWaitUI(t, nil)
	result := appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}
	messages := []appserver.CoordinationWaitMessage{{DeliveryID: "message", SenderTaskName: new("/root/worker")}}
	nativeWaitSettlementFixture(t, u, result, nil, messages)
	nativeWaitSettlementFixture(t, u, result, nil, messages)
	var found []*sessionpresentation.NativeItem
	for _, entry := range u.attached.session.Activity().Main.Entries {
		if item := entry.Source.Native; item != nil && item.Wait != nil {
			found = append(found, item)
		}
	}
	if len(found) != 2 || found[0].Item != "wait-call" || found[1].Item != "native:wait:wait" || found[1].Turn != "first" || found[1].Wait.WaitReason != "message received" {
		t.Fatalf("native wait identity/reason duplicated or lost: %+v", found)
	}
}

func TestLogicalStoppedTargetPublishesWakeReason(t *testing.T) {
	for _, status := range []string{"paused", "failed", "completed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, []appserver.CoordinationWaitOutcome{{Target: appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}, Disposition: status}}, nil)
			nativeTreeFixture(t, threads)
			rows, _ := u.testMainFrame(100, 15, 0)
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Finished waiting for worker: "+status) || strings.Contains(text, status+": completed") {
				t.Fatalf("stopped-target wake invisible: %s", text)
			}
		})
	}
}

func TestUISnapshotLogicalWaitAllOutcome(t *testing.T) {
	for _, reason := range []string{"paused", "delivery failed"} {
		t.Run(reason, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			u.attached.cwd = "/workspace"
			nativeTreeFixture(t, threads)
			result := appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}
			var outcomes []appserver.CoordinationWaitOutcome
			if reason == "paused" {
				outcomes = []appserver.CoordinationWaitOutcome{{Target: appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}, Disposition: "paused"}}
			} else {
				result = appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "dispatchRejected", Diagnostic: new("injection rejected")}}
			}
			nativeWaitSettlementFixture(t, u, result, outcomes, nil, "all")
			nativeTreeFixture(t, threads)
			rows, _ := u.testMainFrame(80, 10, 0)
			// A paused target states its own outcome; delivery failure belongs to the wait.
			want := "Finished waiting for worker: " + reason
			if outcomes == nil {
				want = "Finished waiting for worker ·\u00a0" + reason
			}
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, want) || strings.Contains(text, reason+": completed") {
				t.Fatalf("aggregate wake invisible: %s", text)
			}
			name := "logical-wait-all-outcome"
			if reason == "delivery failed" {
				name += "-delivery-failed"
			}
			assertNativeUISnapshot(t, name, rows)
		})
	}
}

func TestStandaloneCompactionOmitsAgentWaitPresentation(t *testing.T) {
	u, threads, _ := nativeRunningUI(t)
	appServerTestNotify(t, u, "thread/contextReset", sessioncontrol.ContextReset{Thread: "main", Turn: "first", ResetID: "standalone", Status: "started"})
	nativeTreeFixture(t, threads)
	if u.attached.session.Lifecycle("main").Wait != nil {
		t.Fatal("standalone compact published targetless agent wait")
	}
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Wait != nil {
			t.Fatal("standalone continuation published agent-wait outcome")
		}
	}
}

func TestCompactionOutcomeSurvivesWaitCancellation(t *testing.T) {
	for _, outcome := range []string{"completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			appServerTestKeys(t, u, "/compact\r")
			request := appServerOneRequest(t, u.Host.Input.(*appServerTestInput), "thread/compact/start", "")
			compactWaitReset(u, request, "compact", "accepted")
			compactWaitReset(u, request, "compact", outcome)
			nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "inputSuperseded"}}, nil, nil)
			nativeTreeFixture(t, threads)
			for _, entry := range u.attached.source.Entries {
				if strings.Contains(entry.Source.Text, "Compaction canceled") {
					t.Fatalf("settled reset mislabeled after wait cancellation: %q", entry.Source.Text)
				}
			}
		})
	}
}

func TestNativePendingWaitUsesItsActiveTurn(t *testing.T) {
	u, _, _ := nativeWaitUI(t, nil)
	if !u.Busy(false) || u.Turn != "first" || !u.PlanInput(sessioncontrol.QueueNextTurn, false).Queued {
		t.Fatal("pending native wait lost its active turn or queue gate")
	}
	wire := u.Host.Input.(*appServerTestInput)
	if c := nativeTestCancel(t, u); c.quit {
		t.Fatal("wait interruption quit the client")
	}
	request := appServerOneRequest(t, wire, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	if !u.Busy(false) {
		t.Fatal("interrupt acknowledgement completed the native turn")
	}
	appServerTestTurnEnd(t, u, "first", "interrupted")
	if u.Busy(false) {
		t.Fatal("native turn completion left work pending")
	}
}
