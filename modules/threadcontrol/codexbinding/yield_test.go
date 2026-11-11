package codexbinding

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type progressNative struct {
	control.Native
	reads     int
	chunks    int
	contextID string
	driver    *Driver
	requests  int
}

func TestWaitSummaryGuidanceReachesBothToolCatalogs(t *testing.T) {
	for _, claude := range []bool{false, true} {
		found := false
		for _, tool := range ToolCatalog(claude) {
			if tool.Name != "wait_agent" {
				continue
			}
			found = true
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]struct {
					Description string `json:"description"`
					Minimum     int    `json:"minimum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			yield := schema.Properties["yield_time_ms"]
			if yield.Minimum != 300000 || !strings.Contains(yield.Description, "not liveness") || !strings.Contains(yield.Description, "delivery waits for all replies") || !strings.Contains(yield.Description, "omit to disable") {
				t.Fatalf("claude=%t effective yield guidance=%+v", claude, yield)
			}
		}
		if !found {
			t.Fatal("wait catalog missing")
		}
	}
}

type progressRuntime struct {
	control.Runtime
	contextID string
	source    *progressNative
}

func (r progressRuntime) ContextIdentity(string) string { return r.contextID }
func (n *progressNative) Resume(ctx context.Context, thread string) (control.Runtime, error) {
	runtime, err := n.Native.Resume(ctx, thread)
	return progressRuntime{runtime, n.contextID, n}, err
}

func (n *progressNative) Runtime(thread string) (control.Runtime, error) {
	runtime, err := n.Native.Runtime(thread)
	return progressRuntime{runtime, n.contextID, n}, err
}

func (r progressRuntime) Input(ctx context.Context, input control.NativeInput) (string, error) {
	if input.Kind != "PROGRESS_CHECKPOINT" {
		return r.Runtime.Input(ctx, input)
	}
	r.source.requests++
	member, _ := r.source.driver.control.Member(input.Thread)
	waiter, _ := r.source.driver.control.Member("root")
	args, _ := json.Marshal(map[string]string{"target": "agent:main", "message": control.ProgressReplyPrefix + waiter.Wait.Summaries[0].ID + "\nChanged behavior for the requested scope; tests not run."})
	_, err := r.source.driver.Call(ctx, control.Identity{Thread: input.Thread, Session: member.Session, Turn: input.TargetTurn, Call: "summary", Item: "summary"}, "send_message", args)
	return input.TargetTurn, err
}

func (n *progressNative) WaitProgress(_ string, seen map[string]int) (control.WaitProgress, map[string]int) {
	n.reads++
	if seen["capture"] >= n.chunks {
		return control.WaitProgress{}, nil
	}
	return control.WaitProgress{Agents: []control.AgentProgress{{Task: "/root/child", Changes: "amber1"}}}, map[string]int{"capture": n.chunks}
}

func TestWaitYieldDispatchesThroughExistingTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, _, caller := reconciliationFixture(t)
		source := &progressNative{Native: host, contextID: "context", chunks: 1, driver: driver}
		driver.host = source
		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer driver.Stop(t.Context())
		host.inputs = nil
		for i := range 3 {
			if i == 2 {
				source.contextID = "reset"
			}
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
			if i == 1 {
				for range 3 {
					time.Sleep(5 * time.Minute)
					driver.Tick(time.Now(), []string{caller.Thread})
					synctest.Wait()
					member, _ := driver.control.Member(caller.Thread)
					if len(host.inputs) != 1 || member.Wait.ID != caller.Call || member.Wait.State != "registered" || member.Status != control.Waiting || member.HostRunning || driver.control.WaitPublication(caller.Thread).Settled() {
						t.Fatalf("unchanged interval woke inference: inputs=%+v member=%+v", host.inputs, member)
					}
				}
				if use, _, _ := driver.control.FeatureUse(caller.Thread); use.ProgressWaits != 2 || use.ProgressYields != 1 {
					t.Fatal("unchanged interval counted as delivered progress", use)
				}
				// A later interval under the same capture ID must remain eligible.
				source.chunks++
			}
			time.Sleep(5 * time.Minute)
			driver.Tick(time.Now(), []string{caller.Thread})
			synctest.Wait()
			if failures := driver.Failures(); len(failures) != 0 {
				t.Fatal(failures)
			}
			if len(host.inputs) != i+1 {
				t.Fatalf("expected one bounded progress wake: %+v", host.inputs)
			}
			input := host.inputs[i]
			if input.Wake == nil || input.Wake.State != "yielded" || input.Wake.Progress == nil || len(input.Wake.Progress.Agents) != 1 {
				t.Fatalf("yield output: %+v reads=%d", input, source.reads)
			}
			if driver.control.YieldCursor(caller.Thread, source.contextID)["capture"] != source.chunks {
				t.Fatal("delivery did not retain cursor")
			}
			caller.Turn = host.nextTurn
		}
		if use, _, _ := driver.control.FeatureUse(caller.Thread); use.ProgressWaits != 3 || use.ProgressYields != 3 {
			t.Fatal("confirmed progress counts", use)
		}
		if source.reads != 6 || source.requests != 3 {
			t.Fatalf("unexpected progress polling: %d", source.reads)
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

func TestWaitYieldUnavailableProgressStaysPending(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t) // No capture reader.
	caller.Call = "wait-without-progress"
	_, err := driver.control.RegisterWait(t.Context(), caller, control.WaitAny, nil, new(int64(300000)), time.Now().Add(-5*time.Minute))
	if err != nil {
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
	host.inputs = nil
	if err := driver.Wake(t.Context(), caller.Thread, time.Now()); err != nil {
		t.Fatal(err)
	}
	if use, _, _ := driver.control.FeatureUse(caller.Thread); use.ProgressWaits != 1 || use.ProgressYields != 0 {
		t.Fatal("unavailable progress counted", use)
	}
	if len(host.inputs) != 0 || driver.control.WaitPublication(caller.Thread).State != "waiting" {
		t.Fatalf("unavailable captures woke native inference: %+v", host.inputs)
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
			if use, _, _ := driver.control.FeatureUse(caller.Thread); use.ProgressYields != 0 {
				t.Fatal("completion counted as progress", use)
			}
			wake := host.inputs[1].Wake
			if wake.Progress != nil || mode == control.WaitAny && wake.Event == nil || mode == control.WaitAll && len(wake.Events) != 1 {
				t.Fatalf("completion lost its notification: %+v", wake)
			}
		})
	}
}
