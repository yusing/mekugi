package codexbinding

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestHumanChildResultUsesParentDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, _, _ := reconciliationFixture(t)
		if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, ""); err != nil {
			t.Fatal(err)
		}
		if err := driver.DispatchPending(t.Context(), "child"); err != nil {
			t.Fatal(err)
		}
		host.inputs = nil
		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer driver.Stop(t.Context())
		if _, err := driver.HumanInput(t.Context(), "child", "human"); err != nil {
			t.Fatal(err)
		}
		if len(host.inputs) != 0 {
			t.Fatal("human admission injected agent content")
		}
		driver.HumanInputAcknowledged("child", "human", "human-turn", nil)
		if err := driver.Observe("item/completed", []byte(`{"threadId":"child","turnId":"human-turn","item":{"type":"agentMessage","text":"child result","phase":"final_answer"}}`)); err != nil {
			t.Fatal(err)
		}
		if err := driver.Observe("turn/completed", []byte(`{"threadId":"child","turn":{"id":"human-turn","status":"completed"}}`)); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if failures := driver.Failures(); len(failures) != 0 {
			t.Fatal(failures)
		}
		if len(host.inputs) != 1 || host.inputs[0].Kind != "FINAL_ANSWER" || host.inputs[0].Thread != "root" || host.inputs[0].Message != "child result" || host.inputs[0].Run {
			t.Fatalf("parent delivery = %+v", host.inputs)
		}
	})
}

type reconciliationRuntime struct{ control.Runtime }

func (*reconciliationRuntime) Inspect(context.Context, string) (control.SessionDetails, error) {
	return control.SessionDetails{}, nil
}

type reconciliationHost struct {
	control.Native
	reconciliationRuntime
	inputs, confirmed []control.NativeInput
	failure           error
	wakeFailure       error
	finalFailure      error
	missing           bool
	resumes           int
}

func (h *reconciliationHost) Create(context.Context, string, string, control.Settings) (control.Receipt, error) {
	return control.Receipt{Thread: "child", Session: "child-session", Path: "/child-rollout"}, nil
}
func (h *reconciliationHost) Resume(context.Context, string) (control.Runtime, error) {
	h.resumes++
	return h, nil
}
func (h *reconciliationHost) Runtime(string) (control.Runtime, error) { return h, nil }
func (h *reconciliationHost) Input(_ context.Context, input control.NativeInput) (string, error) {
	h.inputs = append(h.inputs, input)
	if input.Kind == "FINAL_ANSWER" && h.finalFailure != nil {
		return "", h.finalFailure
	}
	if input.Wake != nil && h.wakeFailure != nil {
		return "", h.wakeFailure
	}
	if h.failure != nil {
		return "", h.failure
	}
	if input.Run {
		return input.Thread + "-turn", nil
	}
	return "", nil
}
func (h *reconciliationHost) Confirm(_ context.Context, input control.NativeInput) error {
	h.confirmed = append(h.confirmed, input)
	if h.missing {
		return errors.New("retained outcome absent")
	}
	return nil
}
func (*reconciliationHost) Observe(string, string, string, bool) bool { return true }

func reconciliationFixture(t *testing.T) (*Driver, *reconciliationHost, history.Session, control.Identity) {
	t.Helper()
	retained, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	service := control.New()
	root := &control.Member{Thread: "root", Session: "root-session", Task: "/root", Turn: "root-turn", Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true, Limits: control.Limits{MaxWaitMS: 1000}}
	if err := service.AdmitRoot(t.Context(), session, root); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	host := &reconciliationHost{}
	if err := driver.Connect(host, host); err != nil {
		t.Fatal(err)
	}
	caller := control.Identity{Thread: "root", Session: "root-session", Turn: "root-turn", Call: "spawn", Item: "exec"}
	_, err = driver.SpawnPrepared(t.Context(), caller, PreparedChild{TaskName: "child", Message: "original assignment", Settings: control.Settings{Workspace: t.TempDir()}, Limits: root.Limits})
	if err != nil {
		t.Fatal(err)
	}
	return driver, host, session, caller
}

func TestExplicitMessageReconcilesBeforeDependentDispatch(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	host.failure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost injection acknowledgement")}
	caller.Call = "first"
	if _, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"first"}`), "send_message"); err == nil {
		t.Fatal("lost acknowledgement succeeded")
	}
	before := len(host.inputs)
	host.failure, host.missing = nil, true
	caller.Call = "second"
	if _, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"second"}`), "send_message"); err == nil || len(host.inputs) != before {
		t.Fatal("missing retained outcome permitted dependent dispatch")
	}
	host.missing = false
	if _, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"second"}`), "send_message"); err != nil {
		t.Fatal(err)
	}
	if len(host.inputs) != before+1 || host.inputs[before].Message != "second" || host.confirmed[0].ID != "root:first" {
		t.Fatal("reconciliation replayed the original message or changed attribution", host.inputs, host.confirmed)
	}
}

func TestColdRunReconciliationDoesNotRedispatch(t *testing.T) {
	for _, kind := range []string{"initial", "followup"} {
		t.Run(kind, func(t *testing.T) {
			driver, host, session, caller := reconciliationFixture(t)
			caller.Call = "uncertain-run"
			target := "child"
			if kind == "initial" {
				spawn, err := driver.control.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "new", Message: "assignment", Settings: control.Settings{Workspace: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
				target = "new"
				for _, phase := range []string{"create", "assignment_input", "admit"} {
					if _, err := driver.control.ClaimEffect(t.Context(), "root", spawn.ID); err != nil {
						t.Fatal(err)
					}
					if err := driver.control.EffectAcknowledged(t.Context(), "root", spawn.ID, control.Receipt{Thread: target, Session: "new-session", Phase: phase}); err != nil {
						t.Fatal(err)
					}
				}
				host.failure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost acknowledgement")}
				if err := driver.DispatchPending(t.Context(), "root"); err == nil {
					t.Fatal("uncertain initial run succeeded")
				}
				host.failure = nil
			} else {
				host.failure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost acknowledgement")}
				if _, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"assignment"}`), "followup_task"); err == nil {
					t.Fatal("uncertain followup succeeded")
				}
				host.failure = nil
			}
			root, _ := driver.control.Member("root")
			restored := control.New()
			if err := restored.AdmitRoot(t.Context(), session, root); err != nil {
				t.Fatal(err)
			}
			cold, err := NewDriver(restored)
			if err != nil {
				t.Fatal(err)
			}
			if err := cold.Connect(host, host); err != nil {
				t.Fatal(err)
			}
			inputs, resumes := len(host.inputs), host.resumes
			host.missing = true
			if err := cold.Reconcile(t.Context(), target); err == nil {
				t.Fatal("missing retained run was acknowledged")
			}
			host.missing = false
			if err := cold.Reconcile(t.Context(), target); err != nil {
				t.Fatal(err)
			}
			if err := cold.DispatchPending(t.Context(), "root"); err != nil {
				t.Fatal(err)
			}
			member, _ := restored.Member(target)
			tree, err := restored.Snapshot("root")
			if err != nil {
				t.Fatal(err)
			}
			if len(host.inputs) != inputs || host.resumes != resumes || member.Status != control.Paused || member.HostRunning || tree.Effect("root", caller.Call).State != "confirmed" {
				t.Fatal("cold run reconciliation restarted host work", member, host.inputs)
			}
			confirmed := host.confirmed[len(host.confirmed)-1]
			if confirmed.ID != "root:"+caller.Call || !confirmed.Run || confirmed.Thread != target || (confirmed.Settings != nil) != (kind == "initial") {
				t.Fatal("run confirmation lost exact operation identity", confirmed)
			}
		})
	}
}

func TestColdWaitConfirmationDoesNotResumeOrDispatch(t *testing.T) {
	driver, host, session, caller := reconciliationFixture(t)
	driver.host = instructionNative{Native: host, owner: &inspectionRuntime{}}
	caller.Call = "wait"
	if _, err := driver.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, ""); err != nil {
		t.Fatal(err)
	}
	child := control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "message", Item: "child-exec"}
	host.wakeFailure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost wake acknowledgement")}
	if _, err := driver.controlAction(t.Context(), child, []byte(`{"target":"/root","message":"ready"}`), "send_message"); err == nil {
		t.Fatal("uncertain wake succeeded")
	}
	retainedOutput := host.inputs[len(host.inputs)-1].RetainedWaitOutput()
	if display := driver.control.WaitPublication("root"); display.Metrics != nil {
		t.Fatal("display publication exposed frozen metrics")
	}
	*host.inputs[len(host.inputs)-1].Wake.Metrics["/root/child"].ContextTokens = 999
	root, _ := driver.control.Member("root")
	if *root.Wait.Metrics["/root/child"].ContextTokens != 123 {
		t.Fatal("caller mutation changed frozen metrics")
	}
	restored := control.New()
	if err := restored.AdmitRoot(t.Context(), session, root); err != nil {
		t.Fatal(err)
	}
	cold, err := NewDriver(restored)
	if err != nil {
		t.Fatal(err)
	}
	if err := cold.Connect(host, host); err != nil {
		t.Fatal(err)
	}
	inputs, resumes := len(host.inputs), host.resumes
	if _, err := cold.HumanInput(t.Context(), "root", "human"); err != nil {
		t.Fatal(err)
	}
	if err := cold.DispatchPending(t.Context(), "root"); err != nil {
		t.Fatal(err)
	}
	member, _ := restored.Member("root")
	if len(host.inputs) != inputs || host.resumes != resumes || member.Status != control.Paused || member.Wait.State != "completed" || len(restored.Due(time.Now().Add(time.Hour))) != 0 {
		t.Fatal("cold reconciliation restarted work, rearmed a wait or expired its deadline", member)
	}
	last := host.confirmed[len(host.confirmed)-1]
	if last.Wake == nil || last.Wake.ID != "wait" || last.Wake.Event == nil || last.Wake.Event.Message != "ready" {
		t.Fatal("confirmation lost exact retained wake output", last)
	}
	if last.RetainedWaitOutput() != retainedOutput {
		t.Fatal("cold reconciliation changed retained wake identity or metrics")
	}
	if metrics := last.Wake.Metrics["/root/child"]; metrics.ContextTokens == nil || *metrics.ContextTokens != 123 || metrics.Compactions == nil || *metrics.Compactions != 2 {
		t.Fatal("cold confirmation lost dispatched metrics")
	}
}

func TestColdExplicitFollowupLeavesOldMessagesPaused(t *testing.T) {
	driver, host, session, caller := reconciliationFixture(t)
	caller.Call = "old-message"
	if _, err := driver.control.PrepareAction(t.Context(), caller, "child", "message", "old queued message"); err != nil {
		t.Fatal(err)
	}
	root, _ := driver.control.Member("root")
	service := control.New()
	if err := service.AdmitRoot(t.Context(), session, root); err != nil {
		t.Fatal(err)
	}
	caller.Call, caller.Turn = "new-work", "root-resumed-turn"
	if err := service.RequestStarted(t.Context(), caller, true); err != nil {
		t.Fatal(err)
	}
	cold, err := NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	if err := cold.Connect(host, host); err != nil {
		t.Fatal(err)
	}
	inputs, resumes := len(host.inputs), host.resumes
	if _, err := cold.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"new assignment"}`), "followup_task"); err != nil {
		t.Fatal(err)
	}
	tree, err := service.Snapshot("root")
	if err != nil {
		t.Fatal(err)
	}
	if len(host.inputs) != inputs+1 || host.resumes != resumes+1 || !host.inputs[inputs].Run || host.inputs[inputs].Message != "new assignment" || tree.Effect("root", "old-message").State != "paused" {
		t.Fatal("explicit followup restarted old work or redispatched an old message", host.inputs)
	}
}

func TestQueuedHumanInterruptSettlesStoppedLogicalWait(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	caller.Call = "wait"
	if _, err := driver.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, ""); err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer driver.Stop(t.Context())
	driver.Interrupt("root", "stale")
	driver.Interrupt("root", "wait")
	select {
	case <-driver.Changed():
	case <-time.After(3 * time.Second):
		t.Fatal("logical interruption did not publish")
	}
	member, _ := driver.control.Member("root")
	if member.Status != control.Interrupted || member.Reserved() || member.Wait.State != "canceled" {
		t.Fatal("queued human interruption did not settle the logical task", member, driver.Failures())
	}
}

func TestRejectedFinalDeliveryWakesWithVisibleFailure(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	caller.Call = "wait"
	if _, err := driver.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, ""); err != nil {
		t.Fatal(err)
	}
	host.finalFailure = &binding.DispatchError{Outcome: binding.HostRejected, Cause: errors.New("injection rejected")}
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer driver.Stop(t.Context())
	if err := driver.Observe("turn/completed", []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.Changed():
	case <-time.After(3 * time.Second):
		t.Fatal("rejected result did not publish")
	}
	member, _ := driver.control.Member("root")
	last := host.inputs[len(host.inputs)-1]
	if member.Wait.State != "completed" || last.Wake == nil || last.Wake.Event.Kind != "delivery_failed" || len(driver.Failures()) != 1 {
		t.Fatal("rejected result stranded parent or hid delivery failure", member, last)
	}
}
