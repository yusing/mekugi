package launch

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func logicalWaitUI(t *testing.T, cancelSubscription bool) (*appServerUI, *threadExecution, *time.Time) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	retained, err := history.NewService(control.RecordKinds())
	check(err)
	store, err := retained.Open(t.Context(), t.TempDir())
	check(err)
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "main")
	check(err)
	t.Cleanup(session.Close)
	service := control.New()
	check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "main", Session: "session", Task: "/root", Generation: 1, Status: control.Working, AudienceAdmitted: true, Limits: control.Limits{Concurrency: 4, Depth: 3, MaxWaitMS: 1000}}))
	caller := control.Identity{Thread: "main", Session: "session", Turn: "first", Call: "spawn", Item: "exec"}
	check(service.RequestStarted(t.Context(), caller, true))
	spawn, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "worker", Message: "Work", Settings: control.Settings{Workspace: t.TempDir()}, Limits: control.Limits{Concurrency: 4, Depth: 3}})
	check(err)
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		effect, err := service.ClaimEffect(t.Context(), "main", spawn.ID)
		check(err)
		if effect == nil || effect.Phase != phase {
			t.Fatalf("phase %s: %+v", phase, effect)
		}
		check(service.EffectAcknowledged(t.Context(), "main", spawn.ID, control.Receipt{Phase: phase, Thread: "child", Session: "child-session", Turn: "child-turn"}))
	}
	u, at := newActiveWorkTestUI(t)
	u.model, u.reasoningEffort = "test-model", "high"
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first"}})
	caller.Call = "wait"
	_, err = service.RegisterWait(t.Context(), caller, nil, true, new(int64(1000)), *at)
	check(err)
	check(service.Execution(t.Context(), "main", "first", "exec", "", true))
	stop, err := service.ClaimLocalStop(t.Context(), "main", "first", "exec")
	check(err)
	if stop == nil {
		t.Fatal("missing local stop")
	}
	if cancelSubscription {
		_, err = service.CancelWait(t.Context(), caller, caller.Call)
		check(err)
	}
	driver, err := threadbinding.NewDriver(service)
	check(err)
	threads := &threadExecution{shared: service, driver: driver}
	*at = at.Add(2 * time.Second)
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	u.notifications = &nativeNotifications{relay: func(event, _ string) { t.Errorf("local stop emitted notification %s", event) }}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first", "status": "completed"}})
	check(service.HostCompleted(t.Context(), "main", "first", control.Completed, ""))
	return u, threads, at
}

func TestUISnapshotLogicalWaiting(t *testing.T) {
	u, threads, at := logicalWaitUI(t, false)
	appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "main", "status": map[string]any{"type": "idle"}})
	*at = at.Add(8 * time.Second)
	rows, _ := u.testMainFrame(80, 8, 0)
	view := u.attached.presentation.View()
	if view.Status.Composer.Text != "Waiting for agents" || !view.InterruptAvailable || !u.Busy(false) || view.Status.Session.CompletionKnown || view.Status.Session.Elapsed != 10*time.Second {
		t.Fatalf("logical wait lost work: %+v", view.Status)
	}
	agent := u.attached.session.Agent("/root")
	if agent.Final || !agent.Waiting || agent.WorkTimer.At(*at) != 10*time.Second {
		t.Fatalf("logical roster: %+v", agent)
	}
	assertNativeUISnapshot(t, "logical-waiting", rows)
	u.testActivity(u.shell.Agents).publishActivity()
	assertNativeUISnapshot(t, "logical-waiting-roster", u.shell.Agents.NativeRoster(80, 5, *at, true))
	if err := threads.shared.Pause(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	*at = at.Add(time.Hour)
	rows, _ = u.testMainFrame(80, 8, 0)
	if u.Busy(false) || u.attached.presentation.View().Status.Session.Running || u.attached.session.Agent("/root").WorkTimer.At(*at) != 10*time.Second {
		t.Fatal("paused work revived")
	}
	assertNativeUISnapshot(t, "logical-wait-paused", rows)
}

func TestLogicalWaitContinuationPreservesElapsed(t *testing.T) {
	u, threads, at := logicalWaitUI(t, false)
	*at = at.Add(8 * time.Second)
	service := threads.shared
	wake, err := service.PrepareWake(t.Context(), "main", *at)
	if err != nil || wake == nil {
		t.Fatalf("wake: %+v %v", wake, err)
	}
	if _, err := service.ClaimEffect(t.Context(), "main", wake.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "continued"}); err != nil {
		t.Fatal(err)
	}
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
	*at = at.Add(3 * time.Second)
	_, _ = u.testMainFrame(80, 8, 0)
	if got := u.attached.presentation.View().Status.Session.Elapsed; got != 13*time.Second {
		t.Fatalf("elapsed reset across wake: %v", got)
	}
}

func TestLogicalWaitHumanCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "active-subscription", true: "canceled-subscription"}[canceled], func(t *testing.T) {
			u, threads, at := logicalWaitUI(t, canceled)
			if err := threads.driver.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = threads.driver.Stop(t.Context()) })
			if c := nativeTestCancel(t, u); c.quit {
				t.Fatal("wait cancellation quit the client")
			}
			select {
			case <-threads.driver.Changed():
			case <-time.After(3 * time.Second):
				t.Fatal("wait cancellation did not settle")
			}
			if failures := threads.driver.Failures(); len(failures) != 0 {
				t.Fatal(failures)
			}
			syncLogicalWork(&u.attached, &u.Live, threads, *at)
			member, _ := threads.shared.Member("main")
			if member.Status != control.Interrupted || u.LocalStop != "" || u.Busy(false) {
				t.Fatalf("canceled work: %+v", member)
			}
		})
	}
}

func TestUISnapshotLogicalPausedChildren(t *testing.T) {
	u, threads, at := logicalWaitUI(t, false)
	if err := threads.shared.Pause(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	threads.hub = sessionapi.NewHub(t.Context(), nil)
	defer threads.hub.Close()
	views := nativeViews{threads: threads}
	if children := views.descendants(&u.attached, "main"); len(children) != 0 {
		t.Fatal("paused projection started a child runtime")
	}
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	child := u.attached.session.Agent("/root/worker")
	if child == nil || child.Thread != "child" || !child.Paused || child.Responding || child.Waiting {
		t.Fatalf("paused child identity or work was lost: %+v", child)
	}
	u.testActivity(u.shell.Agents).publishActivity()
	assertNativeUISnapshot(t, "logical-paused-children", u.shell.Agents.NativeRoster(80, 5, *at, true))
}

func TestRecoveryInputRetriesWithoutAnotherHostMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, wire := newAppServerTestUI()
		u.ctx = t.Context()
		driver, err := threadbinding.NewDriver(control.New())
		if err != nil {
			t.Fatal(err)
		}
		restored := make(chan error, 1)
		u.attached.threadInput = &threadInput{driver: driver, restoration: restored}
		appServerTestKeys(t, u, "queued during recovery\r")
		if wire.Len() != 0 || len(u.attached.conversation.Unsent) != 1 {
			t.Fatal("input bypassed recovery")
		}
		restored <- nil
		for range 2 {
			if !u.attached.threadInput.preparing() {
				t.Fatal("deferred input lost its tick retry")
			}
			if err := flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, u.ctx, u.restoring != nil, u.controlObservations, u.review, &u.pendingEditReceipts); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
		}
		appServerOneRequest(t, wire, "turn/start", "queued during recovery")
	})
}
