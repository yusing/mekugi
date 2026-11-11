package codexbinding

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type inspectionRuntime struct {
	control.Runtime
	thread  string
	err     error
	metrics *control.SessionMetrics
}

type retainedInspectionNative struct{ control.Native }

func (retainedInspectionNative) Runtime(string) (control.Runtime, error) {
	return nil, errors.New("thread has no running native runtime")
}

func (retainedInspectionNative) Inspect(context.Context, string) (control.SessionDetails, error) {
	return control.SessionDetails{Model: new("retained-model"), Compactions: new(uint64(2))}, nil
}

func TestInspectCompletedAgentWithoutRuntime(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	driver.host = retainedInspectionNative{Native: host}
	resumes := host.resumes
	result, err := driver.inspectAgent(t.Context(), caller, []byte(`{"target":"/root/child"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	want := `{"thread_id":"child","task_name":"/root/child","model":"retained-model","reasoning_effort":null,"service_tier":null,"context_tokens":null,"context_window":null,"compactions":2}`
	if err != nil || string(data) != want || host.resumes != resumes {
		t.Fatalf("completed agent inspection = %s, %v; resumes=%d", data, err, host.resumes)
	}
}

func (r *inspectionRuntime) Inspect(_ context.Context, thread string) (control.SessionDetails, error) {
	r.thread = thread
	details := control.SessionDetails{Model: new("applied-model"), ReasoningEffort: new("high"), ServiceTier: new("priority"), ContextTokens: new(uint64(123)), ContextWindow: new(uint64(1000)), Compactions: new(uint64(2))}
	if r.metrics != nil {
		details.SessionMetrics = *r.metrics
	}
	return details, r.err
}

func TestInspectUsesAdmittedTargetAndRuntimeFacts(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	runtime := &inspectionRuntime{}
	driver.host = instructionNative{Native: host, owner: runtime}
	for _, target := range []string{"/root", "/root/child"} {
		raw, _ := json.Marshal(map[string]string{"target": target})
		result, err := driver.inspectAgent(t.Context(), caller, raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Task   string `json:"task_name"`
			Thread string `json:"thread_id"`
			control.SessionDetails
		}
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("_known")) || !bytes.Contains(encoded, []byte(`"context_window":1000`)) {
			t.Fatalf("public inspection = %s", encoded)
		}
		if got.Task != target || got.Thread != runtime.thread || (got.Model == nil || *got.Model != "applied-model") || *got.ReasoningEffort != "high" || *got.ServiceTier != "priority" || got.ContextTokens == nil || *got.ContextTokens != 123 || got.Compactions == nil || *got.Compactions != 2 {
			t.Fatalf("inspection = %s", encoded)
		}
	}
	runtime.thread = ""
	caller.Call = "followup-with-metrics"
	result, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"continue"}`), "followup_task")
	if err != nil {
		t.Fatal(err)
	}
	if metrics := result.(map[string]any)["metrics"].(control.SessionMetrics); metrics.ContextTokens == nil || metrics.Compactions == nil || *metrics.Compactions != 2 {
		t.Fatalf("followup metrics = %+v", metrics)
	}
	caller.Call = "wait-with-metrics"
	wait, err := driver.waitAgent(t.Context(), caller, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if wait.(control.WaitResult).Metrics != nil {
		t.Fatalf("wait acknowledgement included metrics: %+v", wait)
	}
	runtime.thread = ""
	for _, raw := range []string{`{"target":"/foreign"}`, `{"target":"/root","unknown":true}`, `{}`} {
		if _, err := driver.inspectAgent(t.Context(), caller, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if runtime.thread != "" {
		t.Fatal("invalid target reached runtime")
	}
	runtime.err = errors.New("inactive runtime")
	resumes := host.resumes
	if _, err := driver.inspectAgent(t.Context(), caller, []byte(`{"target":"/root/child"}`)); !errors.Is(err, runtime.err) {
		t.Fatalf("inactive result = %v", err)
	}
	if host.resumes != resumes {
		t.Fatal("inspection resumed work")
	}
	caller.Call = "followup-with-unknown-metrics"
	result, err = driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"continue again"}`), "followup_task")
	if err != nil {
		t.Fatal(err)
	}
	if metrics := result.(map[string]any)["metrics"].(control.SessionMetrics); metrics.ContextTokens != nil || metrics.Compactions != nil {
		t.Fatalf("unavailable metrics = %+v", metrics)
	}
	data, err := json.Marshal(result.(map[string]any)["metrics"])
	if err != nil || string(data) != `{"context_tokens":null,"context_window":null,"compactions":null}` {
		t.Fatalf("unavailable followup wire = %s, err=%v", data, err)
	}
	caller.Call = "wait-without-runtime-metrics"
	wait, err = driver.waitAgent(t.Context(), caller, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if wait.(control.WaitResult).Metrics != nil {
		t.Fatalf("wait acknowledgement included unavailable metrics: %+v", wait)
	}
}

func TestWaitMetricsImmediateCompletion(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	driver.host = instructionNative{Native: host, owner: &inspectionRuntime{}}
	if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	if err := driver.DispatchPending(t.Context(), "child"); err != nil {
		t.Fatal(err)
	}
	caller.Call = "wait"
	result, err := driver.waitAgent(t.Context(), caller, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	wait := result.(control.WaitResult)
	metrics := wait.Metrics["/root/child"]
	if wait.State != "completed" || wait.Event == nil || metrics.ContextTokens == nil || *metrics.ContextTokens != 123 || metrics.Compactions == nil || *metrics.Compactions != 2 {
		t.Fatalf("immediate outcome lost metrics: %+v", wait)
	}
}

func TestWaitMetricsSampleDeliveredOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, args, state, compact string
	}{
		{"completion", `{}`, "completed", ""},
		{"all completions", `{"wait_mode":"all"}`, "completed", ""},
		{"timeout", `{"timeout_ms":1000}`, "timeout", ""},
		{"message", `{}`, "completed", ""},
		{"progress", `{"yield_time_ms":300000}`, "yielded", ""},
		{"unavailable", `{}`, "completed", ""},
		{"compact success", `{"compact":true,"timeout_ms":1000}`, "timeout", "completed"},
		{"compact failure", `{"compact":true,"timeout_ms":1000}`, "timeout", "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver, host, _, caller := reconciliationFixture(t)
			runtime := &inspectionRuntime{}
			driver.host = &progressNative{Native: instructionNative{Native: host, owner: runtime}, chunks: 1}
			caller.Call = "wait"
			if test.compact != "" {
				caller.Cell, caller.Window = "cell", "window"
			}
			result, err := driver.Call(t.Context(), caller, "wait_agent", []byte(test.args))
			if err != nil {
				t.Fatal(err)
			}
			if len(result) != 0 || driver.control.WaitPublication(caller.Thread).Metrics != nil || runtime.thread != "" {
				t.Fatalf("pending wait emitted content or sampled metrics: %s inspected=%q", result, runtime.thread)
			}
			var ticket control.CompoundTicket
			if test.compact != "" {
				member, _ := driver.control.Member(caller.Thread)
				ticket = control.CompoundTicket{WaitID: member.Wait.ID, Generation: member.Wait.Generation}
				result, err := driver.control.SettleCompound(t.Context(), control.CompoundSettlement{Thread: caller.Thread, Turn: caller.Turn, Item: caller.Item, Window: caller.Window, Cell: caller.Cell, Ticket: ticket, Result: "succeeded"})
				if err != nil || result.Action != "commit" {
					t.Fatalf("native stop: %+v, %v", result, err)
				}
			} else {
				if err := driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
					t.Fatal(err)
				}
				if stop, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil || stop == nil {
					t.Fatalf("local stop: %v %v", stop, err)
				}
			}
			if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			if test.compact != "" {
				if err := driver.Wake(t.Context(), caller.Thread, time.Now().Add(time.Hour)); err != nil || runtime.thread != "" {
					t.Fatalf("wake crossed compact barrier: %v inspected=%q", err, runtime.thread)
				}
				receipt := control.CompoundReset{Thread: caller.Thread, Turn: caller.Turn, Item: caller.Item, Window: caller.Window, Cell: caller.Cell, Ticket: ticket, Reset: test.compact}
				if test.compact == "failed" {
					receipt.Diagnostic = "reset failed"
				}
				if err := driver.control.CompoundResetCompleted(t.Context(), receipt); err != nil {
					t.Fatal(err)
				}
			}
			// Facts change while the caller is stopped. Only the delivered outcome samples them.
			runtime.metrics = &control.SessionMetrics{ContextTokens: new(uint64(456)), ContextWindow: new(uint64(1000)), Compactions: new(uint64(3))}
			if test.name == "unavailable" {
				runtime.err = errors.New("inactive runtime")
			}
			switch test.name {
			case "completion", "all completions":
				if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
					t.Fatal(err)
				}
				if err := driver.DispatchPending(t.Context(), "child"); err != nil {
					t.Fatal(err)
				}
			case "message", "unavailable":
				child := control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "message", Item: "child-exec"}
				if _, err := driver.controlAction(t.Context(), child, []byte(`{"target":"/root","message":"ready"}`), "send_message"); err != nil {
					t.Fatal(err)
				}
			}
			if err := driver.Wake(t.Context(), caller.Thread, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if test.name == "progress" {
				if runtime.thread != "" {
					t.Fatal("summary collection sampled final delivery metrics early")
				}
				member, _ := driver.control.Member(caller.Thread)
				slot := member.Wait.Summaries[0]
				if _, err := driver.control.ClaimEffect(t.Context(), caller.Thread, slot.ID); err != nil {
					t.Fatal(err)
				}
				args, _ := json.Marshal(map[string]string{"target": "agent:main", "message": control.ProgressReplyPrefix + slot.ID + "\nFrozen changes; validation not run."})
				if _, err := driver.Call(t.Context(), control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "summary", Item: "summary"}, "send_message", args); err != nil {
					t.Fatal(err)
				}
				if err := driver.Wake(t.Context(), caller.Thread, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			input := host.inputs[len(host.inputs)-1]
			if input.Wake == nil || input.Wake.State != test.state || test.compact != "" && input.Wake.Compaction.State != test.compact {
				t.Fatalf("delivered outcome = %+v", input.Wake)
			}
			// Use the retained host output and normal provider projection, not a synthetic metrics map.
			projected, err := ProjectLocalIdentities(responses.MustMarshalJSON([]any{map[string]any{"type": "function_call_output", "namespace": "threadcontrol", "name": "wait_agent", "output": input.RetainedWaitOutput()}}))
			if err != nil {
				t.Fatal(err)
			}
			var items []struct{ Output string }
			if err := json.Unmarshal(projected, &items, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			var delivered control.WaitResult
			if err := json.Unmarshal([]byte(items[0].Output), &delivered); err != nil {
				t.Fatal(err)
			}
			metrics, found := delivered.Metrics["agent:main/child"]
			if !found || runtime.thread != "child" {
				t.Fatalf("delivered metrics missing: %s inspected=%q", items[0].Output, runtime.thread)
			}
			if test.name == "unavailable" {
				if metrics.ContextTokens != nil || metrics.ContextWindow != nil || metrics.Compactions != nil {
					t.Fatalf("unavailable metrics = %+v", metrics)
				}
			} else if metrics.ContextTokens == nil || *metrics.ContextTokens != 456 || metrics.Compactions == nil || *metrics.Compactions != 3 {
				t.Fatalf("delivered stale metrics: %s", items[0].Output)
			}
		})
	}
}

type cancelInspection struct {
	control.Runtime
	cancel context.CancelFunc
}

func (r *cancelInspection) Inspect(context.Context, string) (control.SessionDetails, error) {
	r.cancel()
	return control.SessionDetails{}, nil
}
func TestMetricsCancellationSettlesUndispatchedWake(t *testing.T) {
	d, host, _, caller := reconciliationFixture(t)
	caller.Call = "wait"
	if _, err := d.waitAgent(t.Context(), caller, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil {
		t.Fatal(err)
	}
	if err := d.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	child := control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "message", Item: "child-exec"}
	action, err := d.control.PrepareAction(t.Context(), child, "/root", "message", "ready")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.control.ClaimEffect(t.Context(), child.Thread, action.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.control.EffectAcknowledged(t.Context(), child.Thread, action.ID, control.Receipt{}); err != nil {
		t.Fatal(err)
	}
	wake, err := d.control.PrepareWake(t.Context(), "root", time.Now())
	if err != nil || wake == nil {
		t.Fatalf("wake %v %v", wake, err)
	}
	claim, err := d.control.ClaimEffect(t.Context(), "root", wake.ID)
	if err != nil || claim == nil {
		t.Fatalf("claim %v %v", claim, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.host = instructionNative{Native: host, owner: &cancelInspection{cancel: cancel}}
	before := len(host.inputs)
	err = d.dispatchClaimedAction(ctx, claim)
	root, _ := d.control.Member("root")
	if err == nil || root.Wait.State != "failed" || len(host.inputs) != before {
		t.Fatalf("error=%v state=%s inputs=%d", err, root.Wait.State, len(host.inputs)-before)
	}
	if _, err := d.HumanInput(t.Context(), "root", "continue"); err != nil {
		t.Fatalf("undispatched wake blocked human input: %v", err)
	}
}
