package codexbinding

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestRecoverableParentFailureWakesOnlyForUnreadEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, _, _ := reconciliationFixture(t)
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		check(driver.Start(t.Context()))
		defer driver.Stop(t.Context())
		fail := func(turn string, waiting bool) {
			t.Helper()
			_, err := driver.Observe("turn/completed", []byte(`{"threadId":"root","turn":{"id":"`+turn+`","status":"failed","error":{"message":"provider at capacity","codexErrorInfo":"serverOverloaded"}}}`))
			check(err)
			synctest.Wait()
			member, _ := driver.control.Member("root")
			if member.HostRunning || (member.Status == control.Waiting) != waiting || member.Reserved() != waiting {
				t.Fatalf("failed parent admission: %+v", member)
			}
			before := len(host.inputs)
			check(driver.Wake(t.Context(), "root", time.Now().Add(24*time.Hour)))
			if len(host.inputs) != before {
				t.Fatal("failure retried without an unread event")
			}
		}
		fail("root-turn", true)
		host.nextTurn = "recovered-message"
		_, err := driver.controlAction(t.Context(), control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "progress", Item: "exec"}, []byte(`{"target":"/root","message":"new progress"}`), "send_message")
		check(err)
		last := host.inputs[len(host.inputs)-1]
		if !last.Run || last.Wake == nil || last.Wake.Event.Kind != "message" {
			t.Fatalf("message did not wake parent: %+v", last)
		}
		fail("recovered-message", true)
		host.nextTurn = "recovered-result"
		_, err = driver.Observe("turn/completed", []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`))
		check(err)
		synctest.Wait()
		last = host.inputs[len(host.inputs)-1]
		if !last.Run || last.Wake == nil || last.Wake.Event.Status != control.Completed {
			t.Fatalf("result did not wake parent: %+v", last)
		}
		fail("recovered-result", false)
		if failures := driver.Failures(); len(failures) != 0 {
			t.Fatal(failures)
		}
	})
}

func TestRecoveryWakesAncestorThroughTerminalCoordinator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, _, _ := reconciliationFixture(t)
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		caller := control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "leaf", Item: "exec"}
		spawn, err := driver.control.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "leaf", Message: "work", Settings: control.Settings{Workspace: t.TempDir()}})
		check(err)
		for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
			_, err := driver.control.ClaimEffect(t.Context(), "child", spawn.ID)
			check(err)
			check(driver.control.EffectAcknowledged(t.Context(), "child", spawn.ID, control.Receipt{Phase: phase, Thread: "leaf", Session: "leaf-session", Turn: "leaf-turn"}))
		}
		check(driver.Start(t.Context()))
		defer driver.Stop(t.Context())
		for index, payload := range []string{
			`{"threadId":"child","turn":{"id":"child-turn","status":"failed"}}`,
			`{"threadId":"root","turn":{"id":"root-turn","status":"failed","error":{"codexErrorInfo":"serverOverloaded"}}}`,
		} {
			_, err := driver.Observe("turn/completed", []byte(payload))
			check(err)
			synctest.Wait()
			if index == 0 {
				check(driver.control.InputConsumed(t.Context(), control.Identity{Thread: "root", Session: "root-session"}, []string{control.MessageItemID("child:1:child-turn:final")}))
			}
		}
		host.nextTurn = "nested-recovery"
		_, err = driver.Observe("turn/completed", []byte(`{"threadId":"leaf","turn":{"id":"leaf-turn","status":"completed"}}`))
		check(err)
		synctest.Wait()
		last := host.inputs[len(host.inputs)-1]
		if !last.Run || last.Thread != "root" || last.Wake == nil || last.Wake.Event.Task != "/root/child/leaf" {
			t.Fatalf("nested completion stranded an ancestor: %+v", last)
		}
	})
}

type blockedRecoveryWakeHost struct {
	*reconciliationHost
	sent    chan struct{}
	release chan struct{}
}

func (h *blockedRecoveryWakeHost) Resume(context.Context, string) (control.Runtime, error) {
	return h, nil
}
func (h *blockedRecoveryWakeHost) Input(ctx context.Context, input control.NativeInput) (string, error) {
	if input.Wake != nil {
		close(h.sent)
		select {
		case <-h.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "recovered", nil
	}
	return h.reconciliationHost.Input(ctx, input)
}
func TestRecoveryPreservesInFlightWakeAcknowledgement(t *testing.T) {
	d, h, _, caller := reconciliationFixture(t)
	blocked := &blockedRecoveryWakeHost{reconciliationHost: h, sent: make(chan struct{}), release: make(chan struct{})}
	d.host = blocked
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	caller.Call = "wait"
	_, err := d.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`))
	check(err)
	check(d.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true))
	_, err = d.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item)
	check(err)
	check(d.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
	check(d.Start(t.Context()))
	defer d.Stop(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := d.controlAction(t.Context(), control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "progress", Item: "exec"}, []byte(`{"target":"/root","message":"new progress"}`), "send_message")
		result <- err
	}()
	<-blocked.sent
	_, err = d.Observe("turn/started", []byte(`{"threadId":"root","turn":{"id":"recovered"}}`))
	check(err)
	reduced, err := d.Observe("turn/completed", []byte(`{"threadId":"root","turn":{"id":"recovered","status":"failed","error":{"message":"overloaded","codexErrorInfo":"serverOverloaded"}}}`))
	check(err)
	check(<-reduced)
	close(blocked.release)
	err = <-result
	t.Logf("send_message result after wake host completion preceded acknowledgement: %v", err)
	if err != nil {
		t.Fatal(err)
	}
}

type pendingRecoveryHost struct {
	*reconciliationHost
	first   chan struct{}
	release chan struct{}
	wakes   int
}

func (h *pendingRecoveryHost) Resume(context.Context, string) (control.Runtime, error) { return h, nil }
func (h *pendingRecoveryHost) Input(ctx context.Context, input control.NativeInput) (string, error) {
	if input.Wake != nil {
		h.wakes++
		if h.wakes == 1 {
			close(h.first)
			select {
			case <-h.release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "recovered", nil
		}
	}
	return h.reconciliationHost.Input(ctx, input)
}
func TestPendingRecoveryDrainsResultAfterAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, h, _, caller := reconciliationFixture(t)
		host := &pendingRecoveryHost{reconciliationHost: h, first: make(chan struct{}), release: make(chan struct{})}
		d.host = host
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		caller.Call = "wait"
		_, err := d.waitAgent(t.Context(), caller, []byte(`{"target":["child"]}`))
		check(err)
		check(d.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true))
		_, err = d.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item)
		check(err)
		check(d.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
		check(d.Start(t.Context()))
		defer d.Stop(t.Context())
		result := make(chan error, 1)
		go func() {
			_, err := d.controlAction(t.Context(), control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "progress", Item: "exec"}, []byte(`{"target":"/root","message":"new progress"}`), "send_message")
			result <- err
		}()
		<-host.first
		_, err = d.Observe("turn/started", []byte(`{"threadId":"root","turn":{"id":"recovered"}}`))
		check(err)
		_, err = d.Observe("turn/completed", []byte(`{"threadId":"root","turn":{"id":"recovered","status":"failed","error":{"codexErrorInfo":"serverOverloaded"}}}`))
		check(err)
		synctest.Wait()
		_, err = d.Observe("turn/completed", []byte(`{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`))
		check(err)
		synctest.Wait()
		close(host.release)
		check(<-result)
		synctest.Wait()
		root, _ := d.control.Member("root")
		t.Logf("wakes=%d root status=%s waitState=%s pendingRecovery=%t failures=%v", host.wakes, root.Status, root.Wait.State, root.RecoveryWait != nil, d.Failures())
		if host.wakes != 2 {
			t.Fatal("result delivered while preceding wake settled did not dispatch recovery continuation")
		}
	})
}
