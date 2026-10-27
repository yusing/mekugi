package launch

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestAppServerCompactPreservesLogicalWait(t *testing.T) {
	for _, test := range []struct {
		userInput   bool
		retryAfter  string
		hostRunning bool
	}{{}, {userInput: true}, {retryAfter: "failed"}, {retryAfter: "interrupted"}, {hostRunning: true}, {hostRunning: true, userInput: true}} {
		t.Run(fmt.Sprintf("userInput=%t/retryAfter=%s/hostRunning=%t", test.userInput, test.retryAfter, test.hostRunning), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setup := logicalWaitUI
				if test.hostRunning {
					setup = logicalWaitRunningUI
				}
				u, threads, at := setup(t, nil)
				u.attached.interruptWait = func(thread, wait string) {
					t.Fatalf("compaction canceled logical wait %s/%s", thread, wait)
				}
				if err := threads.driver.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = threads.driver.Stop(t.Context()) })
				wire := u.Host.Input.(*appServerTestInput)
				wire.Reset()
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
				appServerTestKeys(t, u, "/compact\r")
				if test.hostRunning {
					interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
					notify("turn/completed", `{"threadId":"main","turn":{"id":"first","status":"interrupted"}}`)
					member, _ := threads.shared.Member("main")
					if member.Wait.State != "registered" || member.HostRunning || member.Status != control.Waiting {
						t.Fatalf("prerequisite interruption canceled logical wait: %+v", member)
					}
				}
				request := appServerOneRequest(t, wire, "thread/compact/start", "")
				if test.retryAfter != "" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
					notify("turn/started", `{"threadId":"main","turn":{"id":"first-compact"}}`)
					appServerTestKeys(t, u, "/compact\t")
					notify("turn/completed", fmt.Sprintf(`{"threadId":"main","turn":{"id":"first-compact","status":%q}}`, test.retryAfter))
					if test.retryAfter == "interrupted" {
						if u.Editor.Text != "/compact" {
							t.Fatalf("interruption lost queued command: %q", u.Editor.Text)
						}
						appServerTestKeys(t, u, "\r")
					}
					request = appServerOneRequest(t, wire, "thread/compact/start", "")
				}
				notify("turn/started", `{"threadId":"main","turn":{"id":"compact"}}`)
				if test.userInput {
					appServerTestKeys(t, u, "continue after compact\r")
				}
				if wire.Len() != 0 {
					t.Fatal("input dispatched during compaction")
				}
				notify("item/completed", `{"threadId":"main","turnId":"compact","item":{"id":"compacted","type":"contextCompaction"}}`)
				notify("turn/completed", `{"threadId":"main","turn":{"id":"compact","status":"completed"}}`)
				member, _ := threads.shared.Member("main")
				if member.Status != control.Waiting || member.Turn != "first" || member.HostRunning || member.Wait.State != "registered" || u.attached.session.Agent("/root").Final || u.attached.presentation.View().Status.Session.CompletionKnown {
					t.Fatalf("compaction completed logical work: %+v", member)
				}
				if wire.Len() != 0 || u.Calls() != nil {
					t.Fatal("input overtook compaction acknowledgement")
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
				synctest.Wait()
				if err := flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, t.Context(), false, u.controlObservations, u.review, &u.pendingEditReceipts); err != nil {
					t.Fatal(err)
				}
				if test.userInput {
					appServerOneRequest(t, wire, "turn/start", "continue after compact")
				} else {
					if wire.Len() != 0 || u.attached.presentation.View().Status.Composer.Text != "Waiting for agents" {
						t.Fatalf("compaction replaced selected wait: wire=%s status=%+v", wire.String(), u.attached.presentation.View().Status)
					}
					rows, _ := u.testMainFrame(100, 12, 0)
					if rendered := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(rendered, "Waiting for agents") {
						t.Fatalf("composer lost waiting state after compaction:\n%s", rendered)
					}
					message, err := threads.shared.PrepareAction(t.Context(), control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "message", Item: "exec"}, "/root", "message", "ready")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := threads.shared.ClaimEffect(t.Context(), "child", message.ID); err != nil {
						t.Fatal(err)
					}
					if err := threads.shared.EffectAcknowledged(t.Context(), "child", message.ID, control.Receipt{}); err != nil {
						t.Fatal(err)
					}
					wake, err := threads.shared.PrepareWake(t.Context(), "main", *at)
					if err != nil || wake == nil {
						member, _ := threads.shared.Member("main")
						t.Fatalf("child could not wake after compaction: %+v, %v, member=%+v wait=%+v", wake, err, member, member.Wait)
					}
				}
				if failures := threads.driver.Failures(); len(failures) != 0 {
					t.Fatal(failures)
				}
			})
		})
	}
}

func TestAppServerCancelCompactPreservesLogicalWait(t *testing.T) {
	u, threads, at := logicalWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "/compact\r")
	request := appServerOneRequest(t, wire, "thread/compact/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	appServerTestTurn(t, u, "compact")
	if c := nativeTestCancel(t, u); c.quit {
		t.Fatal("compaction cancellation quit the session")
	}
	interruption := appServerOneRequest(t, wire, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interruption.ID))
	appServerTestTurnEnd(t, u, "compact", "interrupted")
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	member, _ := threads.shared.Member("main")
	if member.Wait.State != "registered" || u.LocalStop == "" || u.Compaction.Pending() || wire.Len() != 0 {
		t.Fatalf("compaction cancellation replaced logical wait: %+v", member)
	}
}
