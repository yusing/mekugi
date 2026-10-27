package codexbinding

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type inspectionRuntime struct {
	control.Runtime
	thread string
	err    error
}

type retainedInspectionNative struct{ control.Native }

func (retainedInspectionNative) Runtime(string) (control.Runtime, error) {
	return nil, errors.New("thread has no running native runtime")
}

func (retainedInspectionNative) Inspect(context.Context, string) (control.SessionDetails, error) {
	return control.SessionDetails{Model: new("retained-model"), SessionMetrics: control.SessionMetrics{Compactions: new(uint64(2))}}, nil
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
	return control.SessionDetails{Model: new("applied-model"), ReasoningEffort: new("high"), ServiceTier: new("priority"), SessionMetrics: control.SessionMetrics{ContextTokens: new(uint64(123)), ContextWindow: new(uint64(1000)), Compactions: new(uint64(2))}}, r.err
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
	wait, err := driver.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if metrics := wait.(control.WaitResult).Metrics["/root/child"]; metrics.ContextTokens == nil || metrics.Compactions == nil || *metrics.Compactions != 2 {
		t.Fatalf("wait metrics = %+v", metrics)
	}
	data, err := json.Marshal(wait.(control.WaitResult).Metrics)
	if err != nil || string(data) != `{"/root/child":{"context_tokens":123,"context_window":1000,"compactions":2}}` {
		t.Fatalf("wait metrics wire = %s, err=%v", data, err)
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
	data, err = json.Marshal(result.(map[string]any)["metrics"])
	if err != nil || string(data) != `{"context_tokens":null,"context_window":null,"compactions":null}` {
		t.Fatalf("unavailable followup wire = %s, err=%v", data, err)
	}
	caller.Call = "wait-without-runtime-metrics"
	wait, err = driver.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(wait.(control.WaitResult).Metrics)
	if err != nil || string(data) != `{"/root/child":{"context_tokens":null,"context_window":null,"compactions":null}}` {
		t.Fatalf("unavailable wait wire = %s, err=%v", data, err)
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
	if _, err := d.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`)); err != nil {
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
