package codexbinding

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestRuntimeDeadlineIncludesMainAfterSideAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, session, caller := reconciliationFixture(t)
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		check(driver.AdmitRequest(t.Context(), session, caller, true))
		reset := time.Now().Add(time.Hour)
		_, err := driver.control.WaitForUsageLimit(t.Context(), caller.Thread, caller.Turn, &reset)
		check(err)
		check(driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
		side, err := session.Store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "side")
		check(err)
		defer side.Close()
		check(driver.control.AdmitRoot(t.Context(), side, &control.Member{Thread: "side", Session: "side-session", Task: "/root", Turn: "side-turn", Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
		check(driver.AdmitRequest(t.Context(), side, control.Identity{Thread: "side", Session: "side-session", Turn: "side-turn"}, true))
		_, err = driver.control.WaitForUsageLimit(t.Context(), "side", "side-turn", &reset)
		check(err)
		check(driver.control.HostCompleted(t.Context(), "side", "side-turn", control.Completed, control.HostFailure{}))
		check(driver.Start(t.Context()))
		defer driver.Stop(t.Context())
		host.inputs = nil
		time.Sleep(time.Hour)
		driver.Tick(time.Now(), []string{"root", "side"})
		synctest.Wait()
		seen := make(map[string]bool)
		for _, input := range host.inputs {
			seen[input.Thread] = input.Run && input.Wake.UsageLimit
		}
		if len(host.inputs) != 2 || !seen["root"] || !seen["side"] {
			t.Fatalf("runtime lost Main or Side deadline: %+v", host.inputs)
		}
	})
}

func TestCanceledUsageLimitWakeIsOrdinaryCancellation(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	reset := time.Now()
	_, err := driver.control.WaitForUsageLimit(t.Context(), caller.Thread, caller.Turn, &reset)
	check(err)
	check(driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
	host.wakeFailure = &binding.DispatchError{Outcome: binding.NotDispatched, Cause: context.Canceled}
	check(driver.Wake(t.Context(), caller.Thread, reset))
	member, _ := driver.control.Member(caller.Thread)
	_, err = driver.control.HumanInterrupt(t.Context(), caller.Thread, member.Stop.Wait)
	check(err)
	member, _ = driver.control.Member(caller.Thread)
	if member.Status != control.Interrupted {
		t.Fatalf("canceled wake kept work active: %+v", member)
	}
}

func TestUsageLimitTimerResumesAdmittedThread(t *testing.T) {
	for _, thread := range []string{"root", "child"} {
		t.Run(thread, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				driver, host, session, _ := reconciliationFixture(t)
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				member, _ := driver.control.Member(thread)
				session.Thread = thread
				check(driver.AdmitRequest(t.Context(), session, control.Identity{Thread: thread, Session: member.Session, Turn: member.Turn}, member.Parent == ""))
				reset := time.Now().Add(time.Hour)
				_, err := driver.control.WaitForUsageLimit(t.Context(), thread, member.Turn, &reset)
				check(err)
				check(driver.Start(t.Context()))
				defer driver.Stop(t.Context())
				host.inputs = nil
				host.nextTurn = "resumed"
				driver.Completed(thread, member.Turn, control.Completed, control.HostFailure{}, nil)
				synctest.Wait()
				if len(host.inputs) != 0 {
					t.Fatal("provider wait resumed before reset")
				}
				time.Sleep(time.Hour)
				driver.Tick(time.Now(), []string{thread})
				synctest.Wait()
				if len(host.inputs) != 1 || !host.inputs[0].Run || !host.inputs[0].Wake.UsageLimit || host.inputs[0].Wake.State != "completed" {
					t.Fatalf("resume=%+v", host.inputs)
				}
				driver.Tick(reset.Add(time.Hour), []string{thread})
				synctest.Wait()
				if len(host.inputs) != 1 {
					t.Fatal("usage limit resumed twice")
				}
			})
		})
	}
}

func TestUsageLimitDuringWakeKeepsDispatchIdentity(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	service := driver.control
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	reset := time.Now().Add(time.Hour)
	_, err := service.WaitForUsageLimit(t.Context(), caller.Thread, caller.Turn, &reset)
	check(err)
	check(service.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
	wake, err := service.PrepareWake(t.Context(), caller.Thread, reset)
	check(err)
	_, err = service.ClaimEffect(t.Context(), caller.Thread, wake.ID)
	check(err)
	caller.Turn = "retry"
	check(service.RequestStarted(t.Context(), caller, true))
	_, err = service.WaitForUsageLimit(t.Context(), caller.Thread, caller.Turn, nil)
	check(err)
	check(service.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
	check(service.EffectAcknowledged(t.Context(), caller.Thread, wake.ID, control.Receipt{Turn: caller.Turn}))
	member, _ := service.Member(caller.Thread)
	if member.HostRunning || member.Status != control.Waiting || member.Wait.Turn != caller.Turn || !member.Wait.UsageLimit || member.RecoveryWait != nil {
		t.Fatalf("retry wait=%+v", member)
	}
	wake, err = service.PrepareWake(t.Context(), caller.Thread, reset.Add(time.Hour))
	check(err)
	if wake != nil {
		t.Fatal("untimed repeated usage limit resumed")
	}
}
