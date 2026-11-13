package launch

import (
	json "encoding/json/v2"
	"fmt"
	"slices"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestAppServerShellWakesLogicalWait(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(completionFirst), func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			notifications := 0
			u.notifications = &nativeNotifications{relay: func(event, _ string) {
				if event == "agent-turn-complete" {
					notifications++
				}
			}}
			wire := u.Host.Input.(*appServerTestInput)
			appServerTestKeys(t, u, "!printf shell-output; exit 7\r")
			request := appServerOneRequest(t, wire, "thread/shellCommand", "")
			ack := fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID)
			if !completionFirst {
				appServerTestMessage(t, u, ack)
			}
			appServerTestTurn(t, u, "shell-and-model")
			appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"shell-and-model","item":{"id":"command","type":"commandExecution","source":"userShell","command":"printf shell-output; exit 7","status":"inProgress"}}}`)
			appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"shell-and-model","item":{"id":"command","type":"commandExecution","source":"userShell","command":"printf shell-output; exit 7","status":"failed","exitCode":7,"aggregatedOutput":"shell-output"}}}`)
			root := threads.native.agent("main")
			if root.Work.WorkID != "main-work" || root.Work.Disposition != "waiting" || root.Wait.WaitID != "wait" || !u.Busy(false) || wire.Len() != 0 || notifications != 0 {
				t.Fatal("shell output settled model work or dispatched another turn")
			}
			output := false
			for _, record := range u.attached.source.Entries {
				output = output || slices.Contains(record.Source.OutputTail, "shell-output")
			}
			if !output {
				t.Fatal("nonzero shell output was lost")
			}
			// Native closes the wait after durable output and samples in this same
			// physical turn. Go never fabricates another human input or turn/start.
			appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{
				WaitID: "wait", CallerThreadID: "main", CallerWorkID: "main-work", Mode: "any",
				Origin:   appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first", ItemID: "exec", CellID: "wait-cell"},
				Selected: []appserver.CoordinationTarget{{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}},
				Result:   appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "shellOutput", TurnID: "shell-and-model"}},
			}}})
			root.Work.Disposition, root.Wait = "running", nil
			nativeTreeFixture(t, threads)
			if u.Turn != "shell-and-model" || u.Waiting != "" || u.NativeWork != "main-work" || u.attached.session.Lifecycle("main").Final || wire.Len() != 0 || notifications != 0 {
				t.Fatal("native shell continuation changed turn identity or completed Main")
			}
			appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"shell-and-model","item":{"id":"answer","type":"agentMessage","text":"Model consumed shell output."}}}`)
			appServerTestTurnEnd(t, u, "shell-and-model", "completed")
			threads.native.agent("main").Work.Disposition = "completed"
			nativeTreeFixture(t, threads)
			if completionFirst {
				appServerTestMessage(t, u, ack)
			}
			if !u.attached.session.Lifecycle("main").Final || u.Busy(false) || notifications != 1 || wire.Len() != 0 {
				t.Fatal("same-turn model final was treated as shell-only completion")
			}
			appServerTestTurnEnd(t, u, "shell-and-model", "completed")
			appServerTestMessage(t, u, ack)
			if wire.Len() != 0 || notifications != 1 {
				t.Fatal("duplicate shell settlement restarted work or repeated completion")
			}
		})
	}
}

func TestAppServerRejectedShellKeepsLogicalWait(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "!pwd\r")
	request := appServerOneRequest(t, wire, "thread/shellCommand", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, request.ID))
	member := threads.native.agent("main")
	if wire.Len() != 0 || member.Work.Disposition != "waiting" || member.Wait.Phase != "registered" || u.Waiting != "wait" || u.Text != "!pwd" {
		data, _ := json.Marshal(member)
		t.Fatalf("rejection changed logical work: %s, draft=%q", data, u.Text)
	}
}

func TestAppServerShellSerializesClaimedWaitWake(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	appServerTestKeys(t, u, "!pwd\r")
	request := appServerOneRequest(t, wire, "thread/shellCommand", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	appServerTestTurn(t, u, "shell-and-model")
	threads.native.agent("child").Work.Disposition = "completed"
	nativeTreeFixture(t, threads)
	if u.Turn != "shell-and-model" || wire.Len() != 0 {
		t.Fatal("ready child wake overlapped shell execution")
	}
	appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"shell-and-model","item":{"id":"command","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}}`)
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"shell-and-model","item":{"id":"command","type":"commandExecution","source":"userShell","command":"pwd","status":"completed","exitCode":0,"aggregatedOutput":"/workspace"}}}`)
	projection := appserver.AgentCoordination{ThreadID: "main", TurnID: "shell-and-model", Projection: appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{
		WaitID: "wait", CallerThreadID: "main", CallerWorkID: "main-work", Mode: "any", ClaimID: new("claim"),
		Origin:   appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first", ItemID: "exec", CellID: "wait-cell"},
		Selected: []appserver.CoordinationTarget{{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}},
		Result:   appserver.CoordinationWaitResult{Kind: "released", Wake: "events"},
		Outcomes: []appserver.CoordinationWaitOutcome{{Target: appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}, Disposition: "completed"}},
	}}}
	appServerTestNotify(t, u, "agent/coordination", projection)
	root := threads.native.agent("main")
	root.Work.Disposition, root.Wait = "running", nil
	nativeTreeFixture(t, threads)
	appServerTestNotify(t, u, "agent/coordination", projection)
	if u.Turn != "shell-and-model" || u.NativeWork != "main-work" || u.Waiting != "" || wire.Len() != 0 {
		t.Fatal("claimed native wake created a second shell continuation")
	}
}

func TestAppServerShellAfterDispatchedWaitWake(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			notifications := 0
			u.notifications = &nativeNotifications{relay: func(event, _ string) {
				if event == "agent-turn-complete" {
					notifications++
				}
			}}
			wire := u.Host.Input.(*appServerTestInput)
			root := threads.native.agent("main")
			if observed {
				root.CanAcceptDirectInput = new(false)
				nativeTreeFixture(t, threads)
			}
			appServerTestKeys(t, u, "!pwd\r")
			if !observed {
				request := appServerOneRequest(t, wire, "thread/shellCommand", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"a turn is being admitted or finalized"}}`, request.ID))
			}
			if wire.Len() != 0 || u.Text != "!pwd" {
				t.Fatalf("shell overtook pending native model admission or lost its draft: wire=%s draft=%q nativePending=%v", wire.String(), u.Text, u.NativeInputPending)
			}
			root = threads.native.agent("main")
			root.CanAcceptDirectInput, root.Work.Disposition, root.Wait = new(true), "running", nil
			nativeTreeFixture(t, threads)
			appServerTestTurn(t, u, "continued")
			appServerTestKeys(t, u, "\r")
			shell := appServerOneRequest(t, wire, "thread/shellCommand", "")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, shell.ID))
			appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"continued","item":{"id":"shell","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}}`)
			appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"continued","item":{"id":"shell","type":"commandExecution","source":"userShell","command":"pwd","status":"completed","exitCode":0,"aggregatedOutput":"/workspace"}}}`)
			appServerTestTurnEnd(t, u, "continued", "completed")
			threads.native.agent("main").Work.Disposition = "completed"
			nativeTreeFixture(t, threads)
			if u.Turn != "" || !u.attached.session.Lifecycle("main").Final || notifications != 1 || wire.Len() != 0 {
				t.Fatal("completed model turn misclassified as standalone shell")
			}
		})
	}
}
