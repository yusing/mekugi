package codexbinding

import (
	"context"
	"errors"
	"fmt"
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
		if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
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
		if _, err := driver.Observe("item/completed", []byte(`{"threadId":"child","turnId":"human-turn","item":{"type":"agentMessage","text":"child result","phase":"final_answer"}}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Observe("turn/completed", []byte(`{"threadId":"child","turn":{"id":"human-turn","status":"completed"}}`)); err != nil {
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
	nextTurn          string
	ask               func(context.Context, *control.Member, string) (string, error)
}

func (h *reconciliationHost) Inspect(ctx context.Context, thread string) (control.SessionDetails, error) {
	return h.reconciliationRuntime.Inspect(ctx, thread)
}

func (h *reconciliationHost) Create(context.Context, string, string, control.Settings) (control.Receipt, error) {
	return control.Receipt{Thread: "child", Session: "child-session", Path: "/child-rollout"}, nil
}
func (h *reconciliationHost) Resume(context.Context, string) (control.Runtime, error) {
	h.resumes++
	return h, nil
}
func (h *reconciliationHost) Runtime(string) (control.Runtime, error) { return h, nil }
func (h *reconciliationHost) SideQuestion(ctx context.Context, target *control.Member, text string) (string, error) {
	return h.ask(ctx, target, text)
}
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
		if h.nextTurn != "" {
			return h.nextTurn, nil
		}
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
func (*reconciliationHost) ObserveAdmitted(string, string, string, bool) bool { return true }

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

type gatedCompletionHost struct {
	*reconciliationHost
	injectionStarted, injectionRelease chan struct{}
	metricsStarted, metricsRelease     chan struct{}
}

func (h *gatedCompletionHost) Resume(context.Context, string) (control.Runtime, error) {
	return h, nil
}

func (h *gatedCompletionHost) Runtime(string) (control.Runtime, error) { return h, nil }

func (h *gatedCompletionHost) Input(ctx context.Context, input control.NativeInput) (string, error) {
	if input.Kind == "FINAL_ANSWER" {
		select {
		case <-h.injectionStarted:
		default:
			close(h.injectionStarted)
			select {
			case <-h.injectionRelease:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	return h.reconciliationHost.Input(ctx, input)
}

func (h *gatedCompletionHost) Inspect(ctx context.Context, _ string) (control.SessionDetails, error) {
	if h.metricsStarted == nil {
		return control.SessionDetails{}, nil
	}
	close(h.metricsStarted)
	select {
	case <-h.metricsRelease:
	case <-ctx.Done():
		return control.SessionDetails{}, ctx.Err()
	}
	return control.SessionDetails{}, nil
}

func TestSelectedCompletionWakesWithoutSchedulerDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, native, _, caller := reconciliationFixture(t)
		native.inputs = nil
		host := &gatedCompletionHost{reconciliationHost: native,
			injectionStarted: make(chan struct{}), injectionRelease: make(chan struct{}),
			metricsStarted: make(chan struct{}), metricsRelease: make(chan struct{}),
		}
		driver.host = host
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		caller.Call = "wait"
		_, err := driver.control.RegisterWait(t.Context(), caller, nil, true, "", nil, time.Now())
		check(err)
		check(driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true))
		_, err = driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item)
		check(err)
		check(driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
		check(driver.Start(t.Context()))
		defer driver.Stop(t.Context())
		started := time.Now()
		_, err = driver.Observe("item/completed", []byte(`{"threadId":"child","turnId":"child-turn","item":{"type":"agentMessage","id":"answer","text":"ready","phase":"final_answer"}}`))
		check(err)
		terminal := []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`)
		_, err = driver.Observe("turn/completed", terminal)
		check(err)
		synctest.Wait()
		select {
		case <-host.injectionStarted:
		default:
			t.Fatal("completion did not begin injection immediately")
		}
		if len(native.inputs) != 0 {
			t.Fatal("continuation crossed injection acknowledgement")
		}
		close(host.injectionRelease)
		synctest.Wait()
		select {
		case <-host.metricsStarted:
		default:
			t.Fatal("acknowledged result did not begin continuation metrics")
		}
		if len(native.inputs) != 1 || native.inputs[0].Kind != "FINAL_ANSWER" {
			t.Fatalf("metrics barrier input=%+v", native.inputs)
		}
		close(host.metricsRelease)
		synctest.Wait()
		if len(native.inputs) != 2 || native.inputs[1].Wake == nil || native.inputs[1].Wake.ID != caller.Call || native.inputs[1].Wake.Event == nil || native.inputs[1].Wake.Event.Source != "child" || native.inputs[1].Wake.Event.Status != control.Completed {
			t.Fatalf("settled prerequisites did not release original wait: %+v", native.inputs)
		}
		if delay := time.Since(started); delay != 0 {
			t.Fatalf("coordination imposed scheduler delay: %v", delay)
		}
		_, err = driver.Observe("turn/completed", terminal)
		check(err)
		synctest.Wait()
		if len(native.inputs) != 2 {
			t.Fatalf("duplicate completion redelivered result: %+v", native.inputs)
		}
		if failures := driver.Failures(); len(failures) != 0 {
			t.Fatal(failures)
		}
	})
}

func TestConcurrentCompletionsDrainBeforeAllWaitWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, native, _, caller := reconciliationFixture(t)
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		caller.Call = "spawn-sibling"
		spawn, err := first.control.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "sibling", Message: "work", Settings: control.Settings{Workspace: t.TempDir()}})
		check(err)
		for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
			_, err := first.control.ClaimEffect(t.Context(), "root", spawn.ID)
			check(err)
			check(first.control.EffectAcknowledged(t.Context(), "root", spawn.ID, control.Receipt{Phase: phase, Thread: "sibling", Session: "sibling-session", Turn: "sibling-turn"}))
		}
		caller.Call = "wait-all"
		_, err = first.control.RegisterWait(t.Context(), caller, nil, true, control.WaitAll, nil, time.Now())
		check(err)
		check(first.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true))
		stop, err := first.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item)
		check(err)
		if stop == nil {
			t.Fatal("all-wait did not commit")
		}
		check(first.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
		native.inputs, native.nextTurn = nil, "continued"
		host := &gatedCompletionHost{reconciliationHost: native, injectionStarted: make(chan struct{}), injectionRelease: make(chan struct{})}
		first.host = host
		second, err := NewDriver(first.control)
		check(err)
		check(second.Connect(host, native))
		check(first.Start(t.Context()))
		defer first.Stop(t.Context())
		check(second.Start(t.Context()))
		defer second.Stop(t.Context())
		complete := func(driver *Driver, thread, turn string) {
			t.Helper()
			_, err := driver.Observe("item/completed", []byte(fmt.Sprintf(`{"threadId":%q,"turnId":%q,"item":{"type":"agentMessage","id":"answer","text":"ready","phase":"final_answer"}}`, thread, turn)))
			check(err)
			_, err = driver.Observe("turn/completed", []byte(fmt.Sprintf(`{"threadId":%q,"turn":{"id":%q,"status":"completed"}}`, thread, turn)))
			check(err)
		}
		complete(first, "child", "child-turn")
		synctest.Wait()
		select {
		case <-host.injectionStarted:
		default:
			t.Fatal("first final did not begin injection")
		}
		complete(second, "sibling", "sibling-turn")
		synctest.Wait()
		pending, err := first.control.Effect("sibling", "sibling:1:sibling-turn:final")
		check(err)
		if pending == nil || pending.State != "pending" || len(native.inputs) != 0 {
			t.Fatal("sibling crossed earlier injection acknowledgement")
		}
		started := time.Now()
		close(host.injectionRelease)
		synctest.Wait()
		if len(native.inputs) != 3 || native.inputs[0].Kind != "FINAL_ANSWER" || native.inputs[0].Sender != "/root/child" || native.inputs[1].Kind != "FINAL_ANSWER" || native.inputs[1].Sender != "/root/sibling" || native.inputs[2].Wake == nil || len(native.inputs[2].Wake.Events) != 2 {
			t.Fatalf("acknowledged injection stranded sibling or original wait: %+v", native.inputs)
		}
		if time.Since(started) != 0 {
			t.Fatal("eligible continuation waited for a timer")
		}
		complete(second, "sibling", "sibling-turn")
		synctest.Wait()
		if len(native.inputs) != 3 {
			t.Fatal("duplicate final observation redelivered results or wake")
		}
		if failures := append(first.Failures(), second.Failures()...); len(failures) != 0 {
			t.Fatal(failures)
		}
	})
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
	if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
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
	if len(host.inputs) != inputs || host.resumes != resumes || member.Status != control.Paused || member.Wait.State != "completed" || len(restored.Due("root", time.Now().Add(time.Hour))) != 0 {
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
	if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
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
	if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	host.finalFailure = &binding.DispatchError{Outcome: binding.HostRejected, Cause: errors.New("injection rejected")}
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer driver.Stop(t.Context())
	if _, err := driver.Observe("turn/completed", []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`)); err != nil {
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

func TestCompletionReceiptFollowsQueuedStartCheckpoint(t *testing.T) {
	driver, _, session, _ := reconciliationFixture(t)
	if err := driver.control.Pause(t.Context(), "child"); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.HostCompleted(t.Context(), "root", "root-turn", control.Interrupted, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	locked, release, stored := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		stored <- session.Store.Transaction(t.Context(), session.Writes, func(*history.Transaction) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Stop(t.Context()) })
	if _, err := driver.Observe("turn/started", []byte(`{"threadId":"root","turn":{"id":"next"}}`)); err != nil {
		t.Fatal(err)
	}
	reduced, err := driver.Observe("turn/completed", []byte(`{"threadId":"root","turn":{"id":"next","status":"completed"}}`))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reduced:
		t.Fatal("receipt preceded blocked checkpoint")
	default:
	}
	close(release)
	if err := <-stored; err != nil {
		t.Fatal(err)
	}
	select {
	case <-reduced:
	case <-time.After(3 * time.Second):
		t.Fatal("checkpoint did not release receipt")
	}
	if member, _ := driver.control.Member("root"); member.Turn != "next" || member.HostRunning || member.Status != control.Completed {
		t.Fatalf("receipt preceded exact disposition: %+v", member)
	}
}

func TestLifecycleStopUnadmittedThread(t *testing.T) {
	driver, err := NewDriver(control.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Restore establishes observation identity before the first provider request
	// admits logical work. Closing that idle host has nothing to pause.
	driver.Restore(history.Session{Thread: "unadmitted"})
	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleStopPausesAdmittedThread(t *testing.T) {
	driver, _, _, _ := reconciliationFixture(t)
	driver.thread = "root"
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := driver.control.Member("root")
	if before.Status != control.Working {
		t.Fatalf("fixture is not running: %+v", before)
	}
	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	member, ok := driver.control.Member("root")
	if !ok || member.Status != control.Paused {
		t.Fatalf("admitted work was not paused: %+v", member)
	}
}

func TestNativeResetFeatureObservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, _, _, caller := reconciliationFixture(t)
		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer driver.Stop(t.Context())
		for _, raw := range []string{
			`{"threadId":"root","item":{"type":"contextCompaction","id":"failed","status":"failed"}}`,
			`{"threadId":"foreign","item":{"type":"contextCompaction","id":"reset"}}`,
			`{"threadId":"root","item":{"type":"contextCompaction","id":"reset","status":"completed"}}`,
			`{"threadId":"root","item":{"type":"contextCompaction","id":"reset","status":"completed"}}`,
		} {
			if _, err := driver.Observe("item/completed", []byte(raw)); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		if use, _, _ := driver.control.FeatureUse(caller.Thread); use.Compactions != 1 {
			t.Fatal(use)
		}
	})
}
