package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

// The test UI dispatches host notifications directly. The connection's reset
// receipt handler feeds this same manual-compaction owner in production.
func compactWaitReset(u *appServerUI, request appServerTurnRequest, turn, status string, generation uint64) {
	u.Compaction.ObserveReset(sessioncontrol.ContextReset{Thread: "main", RequestID: request.Params.ContextResetRequestID,
		Turn: turn, Generation: generation, ResetID: turn, Status: status})
}

func compactTestStart(t *testing.T, u *appServerUI, request appServerTurnRequest, turn string, generation uint64) {
	t.Helper()
	compactWaitReset(u, request, "", "accepted", generation)
	compactWaitReset(u, request, turn, "started", generation)
	appServerTestTurn(t, u, turn)
}

func compactTestEnd(t *testing.T, u *appServerUI, request appServerTurnRequest, turn, status string, generation uint64) {
	t.Helper()
	resetStatus := status
	if status == "interrupted" {
		resetStatus = "canceled"
	}
	compactWaitReset(u, request, turn, resetStatus, generation)
	appServerTestTurnEnd(t, u, turn, status)
}

func compactTestCancelRequest(t *testing.T, wire *appServerTestInput, request appServerTurnRequest) appServerTurnRequest {
	t.Helper()
	cancel := appServerOneRequest(t, wire, "thread/compact/cancel", "")
	if request.Params.ContextResetRequestID == "" || cancel.Params.ContextResetRequestID != request.Params.ContextResetRequestID || cancel.Params.ThreadID != request.Params.ThreadID {
		t.Fatalf("cancellation did not target its manual reset request: start=%+v cancel=%+v", request, cancel)
	}
	return cancel
}

func TestAppServerCompactPreservesLogicalWait(t *testing.T) {
	for _, test := range []struct {
		userInput  bool
		retryAfter string
	}{{}, {userInput: true}, {retryAfter: "failed"}, {retryAfter: "interrupted"}} {
		t.Run(fmt.Sprintf("userInput=%t/retryAfter=%s", test.userInput, test.retryAfter), func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			wire := u.Host.Input.(*appServerTestInput)
			wire.Reset()
			notify := func(method, raw string) {
				t.Helper()
				if err := u.message(appserver.Message{Method: method, Params: []byte(raw)}); err != nil {
					t.Fatal(err)
				}
				threads.native.projectAgent(&u.nativeSession)
			}
			appServerTestKeys(t, u, "/compact\r")
			request := appServerOneRequest(t, wire, "thread/compact/start", "")
			compactWaitReset(u, request, "", "accepted", 1)
			if test.retryAfter != "" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
				compactWaitReset(u, request, "first-compact", "started", 1)
				notify("turn/started", `{"threadId":"main","turn":{"id":"first-compact"}}`)
				appServerTestKeys(t, u, "/compact\t")
				compactWaitReset(u, request, "first-compact", "failed", 1)
				notify("turn/completed", fmt.Sprintf(`{"threadId":"main","turn":{"id":"first-compact","status":%q,"error":{"message":"compact failed","codexErrorInfo":"serverOverloaded"}}}`, test.retryAfter))
				if test.retryAfter == "interrupted" {
					if u.Editor.Text != "/compact" {
						t.Fatalf("interruption lost queued command: %q", u.Editor.Text)
					}
					appServerTestKeys(t, u, "\r")
				}
				request = appServerOneRequest(t, wire, "thread/compact/start", "")
				compactWaitReset(u, request, "", "accepted", 2)
			}
			generation := uint64(1)
			if test.retryAfter != "" {
				generation = 2
			}
			compactWaitReset(u, request, "compact", "started", generation)
			notify("turn/started", `{"threadId":"main","turn":{"id":"compact"}}`)
			if test.userInput {
				appServerTestKeys(t, u, "continue after compact\r")
			}
			if wire.Len() != 0 {
				t.Fatal("input dispatched during compaction")
			}
			notify("item/completed", `{"threadId":"main","turnId":"compact","item":{"id":"compacted","type":"contextCompaction"}}`)
			compactWaitReset(u, request, "compact", "completed", generation)
			notify("turn/completed", `{"threadId":"main","turn":{"id":"compact","status":"completed"}}`)
			u.testMainFrame(100, 12, 0)
			if u.NativeWork != "main-work" || u.Waiting != "wait" || u.attached.session.Lifecycle("main").Final || u.attached.presentation.View().Status.Session.CompletionKnown {
				t.Fatal("auxiliary compaction completed native waiting work")
			}
			if wire.Len() != 0 || u.Calls() != nil {
				t.Fatal("input overtook compaction acknowledgement")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
			if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
				t.Fatal(err)
			}
			if test.userInput {
				appServerOneRequest(t, wire, "turn/start", "continue after compact")
				return
			}
			rows, _ := u.testMainFrame(100, 12, 0)
			if rendered := ansi.Strip(strings.Join(rows, "\n")); wire.Len() != 0 || !strings.Contains(rendered, "Waiting for agents") {
				t.Fatalf("compaction replaced selected wait: wire=%s\n%s", wire.String(), rendered)
			}
			// Native, not a Go effect claim, publishes the continuation. It must
			// remain usable after the auxiliary reset without another Go start.
			root := threads.native.agent("main")
			root.Work.Disposition, root.Wait = "running", nil
			nativeTreeFixture(t, threads)
			notify("turn/started", `{"threadId":"main","turn":{"id":"continued"}}`)
			if u.Waiting != "" || !u.Busy(false) || wire.Len() != 0 {
				t.Fatal("native continuation after compaction lost work or repeated input")
			}
		})
	}
}

func TestAppServerCompactDoesNotStopProvisionalNativeWait(t *testing.T) {
	for _, userInput := range []bool{false, true} {
		t.Run(fmt.Sprint(userInput), func(t *testing.T) {
			u, threads, _ := nativeRunningUI(t)
			root := threads.native.agent("main")
			root.Wait = &appserver.AgentWaitObservation{WaitID: "wait", Phase: "registered", Mode: "any", Selected: []appserver.AgentWaitTarget{{ThreadID: "child", WorkID: "child-work"}}}
			nativeTreeFixture(t, threads)
			wire := u.Host.Input.(*appServerTestInput)
			wire.Reset()
			appServerTestKeys(t, u, "/compact\r")
			request := appServerOneRequest(t, wire, "thread/compact/start", "")
			compactWaitReset(u, request, "", "accepted", 1)
			// Native admission, not a Go interrupt, replaces the old host turn.
			appServerTestTurnEnd(t, u, "first", "interrupted")
			compactWaitReset(u, request, "compact", "started", 1)
			appServerTestTurn(t, u, "compact")
			if userInput {
				appServerTestKeys(t, u, "continue after compact\r")
			}
			compactWaitReset(u, request, "compact", "completed", 1)
			appServerTestTurnEnd(t, u, "compact", "completed")
			threads.native.projectAgent(&u.nativeSession)
			if wire.Len() != 0 || u.Waiting != "" || u.NativeWork != "main-work" || u.attached.session.Lifecycle("main").Final {
				t.Fatal("uncommitted native wait became a stopped or completed assignment")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
			if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
				t.Fatal(err)
			}
			text := ""
			if userInput {
				text = "continue after compact"
			}
			continued := appServerOneRequest(t, wire, "turn/start", text)
			if !userInput && len(continued.Params.Input) != 0 {
				t.Fatal("auxiliary compaction continuation fabricated human text")
			}
		})
	}
}

func TestAppServerCancelCompactPreservesLogicalWait(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "/compact\r")
	request := appServerOneRequest(t, wire, "thread/compact/start", "")
	compactWaitReset(u, request, "", "accepted", 1)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	compactWaitReset(u, request, "compact", "started", 1)
	appServerTestTurn(t, u, "compact")
	if c := nativeTestCancel(t, u); c.quit {
		t.Fatal("compaction cancellation quit the session")
	}
	cancel := compactTestCancelRequest(t, wire, request)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, cancel.ID))
	compactWaitReset(u, request, "compact", "canceled", 1)
	appServerTestTurnEnd(t, u, "compact", "interrupted")
	threads.native.projectAgent(&u.nativeSession)
	if u.Waiting != "wait" || u.NativeWork != "main-work" || u.Compaction.Pending() || wire.Len() != 0 {
		t.Fatal("compaction cancellation replaced native waiting work")
	}
}
