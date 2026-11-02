package launch

import (
	json "encoding/json/v2"
	"fmt"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestTemporaryUsageLimitWaitingAndClose(t *testing.T) {
	for _, command := range []string{"/side", "/oneoff question"} {
		for _, timed := range []bool{false, true} {
			for _, closure := range []string{"explicit", "thread/closed", "thread/archived"} {
				t.Run(fmt.Sprintf("%s/%t/%s", command, timed, closure), func(t *testing.T) {
					check := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					u, wire := newAppServerTestUI()
					defer terminalTest(u).Diff.Close()
					appServerTestKeys(t, u, command+"\r")
					btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"side"}}`)
					if command == "/side" {
						appServerTestKeys(t, u, "question\r")
					}
					btwTestReply(t, u, btwTestRequest(t, wire, "turn/start", "side"), `{"turn":{"id":"side-turn"}}`)
					retained, err := history.NewService(control.RecordKinds())
					check(err)
					store, err := retained.Open(t.Context(), t.TempDir())
					check(err)
					session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "side")
					check(err)
					defer session.Close()
					service := control.New()
					check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "side", Session: "session", Task: "/root", Generation: 1, Turn: "side-turn", Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
					driver, err := threadbinding.NewDriver(service)
					check(err)
					check(driver.Start(t.Context()))
					defer driver.Stop(t.Context())
					u.Requests.Coordination, u.Requests.InterruptWait = service, driver.Interrupt
					syncLogicalWork(&u.attached, &u.Live, &threadExecution{shared: service, driver: driver}, time.Now())
					await := func() {
						t.Helper()
						select {
						case <-driver.Changed():
						case <-time.After(5 * time.Second):
							t.Fatal("logical lifecycle did not settle")
						}
					}
					deliverCompletion := func(turn string) {
						t.Helper()
						params, err := json.Marshal(map[string]any{"threadId": "side", "turn": map[string]any{"id": turn, "status": "completed"}})
						check(err)
						reduced, err := driver.Observe("turn/completed", params)
						check(err)
						message := appserver.Message{Method: "turn/completed", Params: params}
						deferred, err := deferLogicalCompletion(&u.attached, &u.Live, message, reduced, u.Requests)
						check(err)
						if !deferred {
							t.Fatal("temporary completion bypassed logical checkpoint")
						}
						await()
						check(u.attached.threadInput.flushCompletions(u.message))
					}
					reset := time.Now().Add(time.Hour)
					var deadline *time.Time
					if timed {
						deadline = &reset
					}
					_, err = service.WaitForUsageLimit(t.Context(), "side", "side-turn", deadline)
					check(err)
					deliverCompletion("side-turn")
					u.sideFrame(100, 24)
					status := u.attached.presentation.View().Status.Composer
					if !status.Running || !strings.Contains(strings.ToLower(status.Text), "usage limit") || !u.side.Busy || u.side.Turn != "" {
						t.Fatalf("temporary wait settled: status=%+v side=%+v", status, u.side)
					}
					if timed {
						wake, err := service.PrepareWake(t.Context(), "side", reset)
						check(err)
						_, err = service.ClaimEffect(t.Context(), "side", wake.ID)
						check(err)
						check(service.EffectAcknowledged(t.Context(), "side", wake.ID, control.Receipt{Turn: "continued"}))
						appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "side", "turn": map[string]any{"id": "continued"}})
						if u.side.Waiting != "" || !u.side.Busy || u.side.Turn != "continued" {
							t.Fatal("continuation lost temporary conversation state")
						}
						_, err = service.WaitForUsageLimit(t.Context(), "side", "continued", &reset)
						check(err)
						deliverCompletion("continued")
					}
					if closure == "explicit" {
						check(closeSide(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, &u.side, &u.ownedImages))
						btwTestReply(t, u, btwTestRequest(t, wire, "thread/archive", "side"), `{}`)
					} else {
						appServerTestNotify(t, u, closure, map[string]any{"threadId": "side"})
						if !u.side.Unloaded() || !u.RetiredThread("side", "") {
							t.Fatal("native closure did not retire temporary conversation")
						}
					}
					await()
					wake, err := service.PrepareWake(t.Context(), "side", reset.Add(time.Hour))
					check(err)
					if wake != nil {
						t.Fatal("closed temporary conversation retained its provider wake")
					}
				})
			}
		}
	}
}

func TestTemporaryUsageLimitExplicitInputSupersedesWait(t *testing.T) {
	for _, command := range []string{"/btw question", "/side", "/oneoff question"} {
		for _, resumed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", command, resumed), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					check := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					u, wire := newAppServerTestUI()
					u.attached.ctx = t.Context()
					defer terminalTest(u).Diff.Close()
					appServerTestKeys(t, u, command+"\r")
					btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"side"}}`)
					if command == "/side" {
						appServerTestKeys(t, u, "question\r")
					}
					btwTestReply(t, u, btwTestRequest(t, wire, "turn/start", "side"), `{"turn":{"id":"side-turn"}}`)
					b := u.side
					if command == "/btw question" {
						b = u.BTW
					}
					retained, err := history.NewService(control.RecordKinds())
					check(err)
					store, err := retained.Open(t.Context(), t.TempDir())
					check(err)
					session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "side")
					check(err)
					defer session.Close()
					service := control.New()
					check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "side", Session: "session", Task: "/root", Generation: 1, Turn: "side-turn", Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
					driver, err := threadbinding.NewDriver(service)
					check(err)
					u.Requests.Coordination = service
					syncLogicalWork(&u.attached, &u.Live, &threadExecution{shared: service, driver: driver}, time.Now())
					_, err = service.WaitForUsageLimit(t.Context(), "side", "side-turn", nil)
					check(err)
					check(service.HostCompleted(t.Context(), "side", "side-turn", control.Completed, control.HostFailure{}))
					appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "completed"}})
					b.Drafts.Queued = append(b.Drafts.Queued, conversationmodel.Input{Text: "queued follow-up"})
					flush := func() {
						check(flushBTW(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, preferences.Applied{}, &u.side, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations))
					}
					flush()
					if wire.Len() != 0 || b.Waiting == "" {
						t.Fatal("queued input superseded the usage wait")
					}
					if resumed {
						check(service.RequestStarted(t.Context(), control.Identity{Thread: "side", Session: "session", Turn: "timer-resumed"}, true))
					}
					text := "new instruction"
					if command == "/btw question" {
						text = "/btw " + text
					}
					appServerTestKeys(t, u, text+"\r")
					synctest.Wait()
					flush()
					method := "turn/start"
					if resumed {
						method = "turn/steer"
					}
					request := btwTestRequest(t, wire, method, "side")
					if !strings.Contains(fmt.Sprint(request.Params.Input), "new instruction") || b.Waiting != "" || len(b.Drafts.Queued) != 1 || b.Drafts.Queued[0].Text != "queued follow-up" {
						t.Fatal("explicit input did not supersede waiting while retaining the queue")
					}
					if resumed && request.Params.ExpectedTurnID != "timer-resumed" {
						t.Fatal("input did not steer the already-resumed host turn")
					}
					if resumed && request.Params.Environments != nil {
						t.Fatal("steering included start-only environment settings")
					}
					member, _ := service.Member("side")
					if member.Stop != nil || member.Wait != nil && member.Wait.State != "canceled" {
						t.Fatal("explicit input left the usage stop armed")
					}
					btwTestReply(t, u, request, `{"turn":{"id":"new-turn"}}`)
					if len(u.attached.sideInputs) != 0 {
						t.Fatal("submitted side input retained its admission")
					}
				})
			})
		}
	}
}

func TestUsageLimitOneoffPreservesIndependentChildReplies(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprint(running), func(t *testing.T) {
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			u, wire := newAppServerTestUI()
			defer terminalTest(u).Diff.Close()
			u.startSession("main", "/workspace")
			oneoffTestStart(t, u, wire)
			oneoffTestChild(t, u)
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "child-turn"}})
			retained, err := history.NewService(control.RecordKinds())
			check(err)
			store, err := retained.Open(t.Context(), t.TempDir())
			check(err)
			session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "oneoff")
			check(err)
			defer session.Close()
			service := control.New()
			check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "oneoff", Session: "session", Task: "/root", Generation: 1, Turn: "oneoff-turn", Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
			u.Requests.Coordination = service
			_, err = service.WaitForUsageLimit(t.Context(), "oneoff", "oneoff-turn", nil)
			check(err)
			check(service.HostCompleted(t.Context(), "oneoff", "oneoff-turn", control.Completed, control.HostFailure{}))
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
			if u.side.Waiting == "" {
				t.Fatal("usage limit did not pause One-off")
			}
			oneoffTestAsync(t, u, "oneoff-child", "child-turn", "child-question")
			if !running {
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "child-turn", "status": "completed"}})
			}
			u.side.Drafts.Unsent = append(u.side.Drafts.Unsent, conversationmodel.Input{Text: "next root input"})
			selectSideThread(t, u, u.attached.presentation.View().Main)
			oneoffTestAnswer(t, u)
			method := "turn/steer"
			if !running {
				method = "turn/start"
			}
			btwTestRequest(t, wire, method, "oneoff-child")
			if u.side.Waiting == "" || len(u.side.Drafts.Unsent) != 1 || u.side.Drafts.Unsent[0].Text != "next root input" {
				t.Fatal("child reply consumed root input or canceled its wait")
			}
		})
	}
}
