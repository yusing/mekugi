package launch

import (
	json "encoding/json/v2"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestAppServerShellWakesLogicalWait(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(completionFirst), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, threads, at := logicalWaitUI(t, nil)
				if err := threads.driver.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = threads.driver.Stop(t.Context()) })
				wire := u.Host.Input.(*appServerTestInput)
				wire.Reset()
				appServerTestKeys(t, u, "!printf shell-output; exit 7\r")
				request := appServerOneRequest(t, wire, "thread/shellCommand", "")
				ack := fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID)
				if !completionFirst {
					appServerTestMessage(t, u, ack)
				}
				notify := func(method, raw string) {
					t.Helper()
					message := appserver.Message{Method: method, Params: []byte(raw)}
					syncLogicalWork(&u.attached, &u.Live, threads, *at)
					if err := observeLogicalHost(threads.driver, &u.Live, message); err != nil {
						t.Fatal(err)
					}
					if err := u.message(message); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
				}
				notify("turn/started", `{"threadId":"main","turn":{"id":"shell"}}`)
				notify("item/started", `{"threadId":"main","turnId":"shell","item":{"id":"command","type":"commandExecution","source":"userShell","command":"printf shell-output; exit 7","status":"inProgress"}}`)
				notify("item/completed", `{"threadId":"main","turnId":"shell","item":{"id":"command","type":"commandExecution","source":"userShell","command":"printf shell-output; exit 7","status":"failed","exitCode":7,"aggregatedOutput":"shell-output"}}`)
				member, _ := threads.shared.Member("main")
				if member.Status != control.Waiting || member.Turn != "first" || member.HostRunning || u.Calls() != nil || wire.Len() != 0 {
					t.Fatalf("shell acquired logical model work: %+v, wire=%s", member, wire.String())
				}
				notify("turn/completed", `{"threadId":"main","turn":{"id":"shell","status":"completed"}}`)
				if completionFirst {
					if wire.Len() != 0 || u.Calls() != nil {
						t.Fatal("continuation overtook shell acknowledgement")
					}
					appServerTestMessage(t, u, ack)
					synctest.Wait()
				}
				if err := flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, t.Context(), false, u.controlObservations, u.review, &u.pendingEditReceipts); err != nil {
					t.Fatal(err)
				}
				continuation := appServerOneRequest(t, wire, "turn/start", "")
				if len(continuation.Params.Input) != 0 {
					t.Fatal("shell continuation fabricated human text")
				}
				member, _ = threads.shared.Member("main")
				if member.Status == control.Completed || member.Stop != nil || member.Wait.State != "canceled" || u.attached.session.Agent("/root").Final || u.attached.presentation.View().Status.Session.CompletionKnown {
					t.Fatalf("shell completed Main or retained the wait: %+v", member)
				}
				output := false
				for _, record := range u.attached.source.Entries {
					output = output || slices.Contains(record.Source.OutputTail, "shell-output")
				}
				if !output {
					t.Fatal("nonzero shell output was lost")
				}
				notify("turn/completed", `{"threadId":"main","turn":{"id":"shell","status":"completed"}}`)
				appServerTestMessage(t, u, ack)
				if wire.Len() != 0 || len(threads.driver.Failures()) != 0 {
					t.Fatal("duplicate shell settlement restarted model work")
				}
			})
		})
	}
}

func TestAppServerRejectedShellKeepsLogicalWait(t *testing.T) {
	u, threads, _ := logicalWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "!pwd\r")
	request := appServerOneRequest(t, wire, "thread/shellCommand", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, request.ID))
	member, _ := threads.shared.Member("main")
	if wire.Len() != 0 || u.Shell.Continuation() || member.Status != control.Waiting || member.Wait.State != "registered" || u.Text != "!pwd" {
		data, _ := json.Marshal(member)
		t.Fatalf("rejection changed logical work: %s, draft=%q", data, u.Text)
	}
}

func TestAppServerShellSerializesClaimedWaitWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, threads, at := logicalWaitUI(t, nil)
		wire := u.Host.Input.(*appServerTestInput)
		wire.Reset()
		appServerTestKeys(t, u, "!pwd\r")
		request := appServerOneRequest(t, wire, "thread/shellCommand", "")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
		appServerTestTurn(t, u, "shell")
		if err := threads.shared.Pause(t.Context(), "child"); err != nil {
			t.Fatal(err)
		}
		wake, err := threads.shared.PrepareWake(t.Context(), "main", *at)
		if err != nil || wake == nil {
			t.Fatalf("wake: %+v, %v", wake, err)
		}
		claimed, err := threads.shared.ClaimEffect(t.Context(), "main", wake.ID)
		if err != nil || claimed == nil {
			t.Fatalf("claim: %+v, %v", claimed, err)
		}
		result := make(chan error, 1)
		go func() {
			_, err := u.Requests.Input(t.Context(), control.NativeInput{Thread: "main", Run: true, ID: claimed.ID, Wake: claimed.Wake})
			result <- err
		}()
		synctest.Wait()
		syncLogicalWork(&u.attached, &u.Live, threads, *at)
		if u.Calls() != nil || wire.Len() != 0 {
			t.Fatal("selected child wake overlapped shell execution")
		}
		appServerTestTurnEnd(t, u, "shell", "completed")
		if wire.Len() != 0 || u.Shell.Continuation() {
			t.Fatal("shell created a second continuation beside the claimed wake")
		}
		u.Dispatch(<-u.Calls())
		continuation := appServerOneRequest(t, wire, "turn/start", "")
		if !u.CompleteCall(appserver.Message{ID: []byte(fmt.Sprint(continuation.ID)), Result: []byte(`{"turn":{"id":"continued"}}`)}) {
			t.Fatal("wake acknowledgement escaped its sole request owner")
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if err := threads.shared.EffectAcknowledged(t.Context(), "main", claimed.ID, control.Receipt{Turn: "continued"}); err != nil {
			t.Fatal(err)
		}
		if wire.Len() != 0 {
			t.Fatal("wake settlement repeated continuation")
		}
	})
}

func TestAppServerShellAfterDispatchedWaitWake(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, threads, at := logicalWaitUI(t, nil)
				notifications := 0
				u.notifications = &nativeNotifications{relay: func(event, _ string) {
					if event == "agent-turn-complete" {
						notifications++
					}
				}}
				if err := threads.driver.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = threads.driver.Stop(t.Context()) })
				wire := u.Host.Input.(*appServerTestInput)
				wire.Reset()
				if err := threads.shared.Pause(t.Context(), "child"); err != nil {
					t.Fatal(err)
				}
				wake, err := threads.shared.PrepareWake(t.Context(), "main", *at)
				if err != nil || wake == nil {
					t.Fatalf("wake: %+v %v", wake, err)
				}
				claimed, err := threads.shared.ClaimEffect(t.Context(), "main", wake.ID)
				if err != nil || claimed == nil {
					t.Fatalf("claim: %+v %v", claimed, err)
				}
				result := make(chan error, 1)
				go func() {
					_, err := u.Requests.Input(t.Context(), control.NativeInput{Thread: "main", Run: true, ID: claimed.ID, Wake: claimed.Wake})
					result <- err
				}()
				synctest.Wait()
				u.Dispatch(<-u.Calls())
				continuation := appServerOneRequest(t, wire, "turn/start", "")
				ack := func() {
					t.Helper()
					if !u.CompleteCall(appserver.Message{ID: []byte(fmt.Sprint(continuation.ID)), Result: []byte(`{"turn":{"id":"continued"}}`)}) {
						t.Fatal("missing wake call")
					}
					if err := <-result; err != nil {
						t.Fatal(err)
					}
					if err := threads.shared.EffectAcknowledged(t.Context(), "main", claimed.ID, control.Receipt{Turn: "continued"}); err != nil {
						t.Fatal(err)
					}
				}
				if acknowledged {
					ack()
				}
				appServerTestKeys(t, u, "!pwd\r")
				if wire.Len() != 0 || u.Text != "!pwd" {
					t.Fatal("shell overtook the dispatched model start")
				}
				notify := func(method, params string) {
					t.Helper()
					msg := appserver.Message{Method: method, Params: []byte(params)}
					syncLogicalWork(&u.attached, &u.Live, threads, *at)
					if err := observeLogicalHost(threads.driver, &u.Live, msg); err != nil {
						t.Fatal(err)
					}
					if err := u.message(msg); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
				}
				notify("turn/started", `{"threadId":"main","turn":{"id":"continued"}}`)
				if !acknowledged {
					ack()
				}
				appServerTestKeys(t, u, "\r")
				shell := appServerOneRequest(t, wire, "thread/shellCommand", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, shell.ID))
				notify("item/started", `{"threadId":"main","turnId":"continued","item":{"id":"shell","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}`)
				notify("turn/completed", `{"threadId":"main","turn":{"id":"continued","status":"completed"}}`)
				member, _ := threads.shared.Member("main")
				if member.HostRunning || member.Status != control.Completed || notifications != 1 {
					t.Fatalf("completed model turn misclassified as shell: %+v", member)
				}
			})
		})
	}
}
