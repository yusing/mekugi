package codexbinding

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type progressNative struct {
	control.Native
	reads int
}

func (n *progressNative) WaitProgress(_ string, seen map[string]int) (control.WaitProgress, map[string]int) {
	n.reads++
	if seen["capture"] != 0 {
		return control.WaitProgress{}, nil
	}
	return control.WaitProgress{Agents: []control.AgentProgress{{Task: "/root/child", Status: "working", Activity: "testing", Changes: "mchanges amber1 --summary"}}}, map[string]int{"capture": 1}
}

func TestWaitYieldDispatchesThroughExistingTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, _, caller := reconciliationFixture(t)
		source := &progressNative{Native: host}
		driver.host = source
		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer driver.Stop(t.Context())
		for i := range 2 {
			caller.Call += "-wait"
			if _, err := driver.waitAgent(t.Context(), caller, []byte(`{"yield_time_ms":300000}`)); err != nil {
				t.Fatal(err)
			}
			if err := driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
				t.Fatal(err)
			}
			if stop, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil || stop == nil {
				t.Fatalf("stop: %v %v", stop, err)
			}
			if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			host.nextTurn = caller.Turn + "-next"
			time.Sleep(5 * time.Minute)
			driver.Tick(time.Now(), []string{caller.Thread})
			synctest.Wait()
			if failures := driver.Failures(); len(failures) != 0 {
				t.Fatal(failures)
			}
			input := host.inputs[len(host.inputs)-1]
			if input.Wake == nil || input.Wake.State != "yielded" || input.Wake.Progress == nil || source.reads != i+1 || len(input.Wake.Progress.Agents) != 1-i {
				t.Fatalf("yield output: %+v reads=%d", input, source.reads)
			}
			if driver.control.YieldCursor(caller.Thread)["capture"] != 1 {
				t.Fatal("delivery did not retain cursor")
			}
			caller.Turn = host.nextTurn
		}
	})
}

func TestWaitYieldValidationPreservesPriorState(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	for _, raw := range []string{`{"yield_time_ms":0}`, `{"yield_time_ms":299999}`, `{"yield_time_ms":-1}`, `{"yield_time_ms":9223372036854775807}`} {
		if _, err := driver.waitAgent(t.Context(), caller, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		if member, _ := driver.control.Member(caller.Thread); member.Wait != nil {
			t.Fatal("rejection registered a wait")
		}
	}
}

func TestWaitYieldPrefersCompletionAfterPendingInjection(t *testing.T) {
	for _, mode := range []control.WaitMode{control.WaitAny, control.WaitAll} {
		t.Run(string(mode), func(t *testing.T) {
			driver, host, _, caller := reconciliationFixture(t)
			caller.Call = "pending-result-wait"
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			_, err := driver.control.RegisterWait(t.Context(), caller, mode, nil, new(int64(300000)), now.Add(-5*time.Minute))
			check(err)
			check(driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true))
			stop, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item)
			check(err)
			if stop == nil {
				t.Fatal("missing stop")
			}
			check(driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
			check(driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}))
			host.inputs, host.nextTurn = nil, "continued"
			check(driver.Wake(t.Context(), caller.Thread, now))
			if len(host.inputs) != 2 || host.inputs[0].Kind != "FINAL_ANSWER" || host.inputs[1].Wake == nil || host.inputs[1].Wake.State != "completed" {
				t.Fatalf("completion lost precedence: %+v", host.inputs)
			}
			wake := host.inputs[1].Wake
			if wake.Progress != nil || mode == control.WaitAny && wake.Event == nil || mode == control.WaitAll && len(wake.Events) != 1 {
				t.Fatalf("completion lost its notification: %+v", wake)
			}
		})
	}
}
