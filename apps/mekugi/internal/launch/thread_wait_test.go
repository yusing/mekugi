package launch

import (
	"context"
	json "encoding/json/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"

	"github.com/yusing/mekugi/libraries/diffrender"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func logicalWaitUI(t *testing.T, timeout *int64) (*appServerUI, *threadExecution, *time.Time) {
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
	check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "main", Session: "session", Task: "/root", Generation: 1, Status: control.Working, AudienceAdmitted: true, Limits: control.Limits{MaxWaitMS: 1000}}))
	caller := control.Identity{Thread: "main", Session: "session", Turn: "first", Call: "spawn", Item: "exec"}
	check(service.RequestStarted(t.Context(), caller, true))
	spawn, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "worker", Message: "Work", Settings: control.Settings{Workspace: t.TempDir()}, Limits: control.Limits{}})
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
	registration, err := service.RegisterWait(t.Context(), caller, nil, true, timeout, *at)
	check(err)
	payload, err := json.Marshal(registration)
	check(err)
	envelope, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(payload)}}})
	check(err)
	args := `{}`
	if timeout != nil {
		args = `{"timeout_ms":1000}`
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "first", "item": binding.ThreadItem{ID: "wait-item", Type: "mcpToolCall", Server: "threadcontrol", Tool: "wait_agent", Status: "completed", Arguments: []byte(args), Result: envelope}})
	check(service.Execution(t.Context(), "main", "first", "exec", "", true))
	stop, err := service.ClaimLocalStop(t.Context(), "main", "first", "exec")
	check(err)
	if stop == nil {
		t.Fatal("missing local stop")
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
	u, threads, at := logicalWaitUI(t, new(int64(1000)))
	u.attached.cwd = "/workspace"
	appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "main", "status": map[string]any{"type": "idle"}})
	*at = at.Add(8 * time.Second)
	rows, _ := u.testMainFrame(80, 8, 0)
	view := u.attached.presentation.View()
	if view.Status.Composer.Text != "Waiting for agents" || !view.InterruptAvailable || !u.Busy(false) || view.Status.Session.CompletionKnown || at.Sub(view.Status.Session.Started) != 10*time.Second {
		t.Fatalf("logical wait lost work: %+v", view.Status)
	}
	agent := u.attached.session.Agent("/root")
	if agent.Final || !agent.Waiting || agent.WorkTimer.At(*at) != 10*time.Second {
		t.Fatalf("logical roster: %+v", agent)
	}
	assertNativeUISnapshot(t, "logical-waiting", rows)
	u.testActivity(u.shell.Agents).publishActivity()
	if text := ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 5, *at, true), "\n")); !strings.Contains(text, "Waiting for all (timeout 1s)") {
		t.Fatalf("roster lost transcript wait details: %s", text)
	}
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
	u, threads, at := logicalWaitUI(t, new(int64(1000)))
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
	before := len(u.attached.source.Entries)
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	if len(u.attached.source.Entries) != before {
		t.Fatal("wake repeated its completion notice")
	}
	rows, _ := u.testMainFrame(100, 15, 0)
	text := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(text, "Finished waiting for all: timeout") {
		t.Fatalf("invisible timeout wake: %s", text)
	}
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
	*at = at.Add(3 * time.Second)
	_, _ = u.testMainFrame(80, 8, 0)
	if got := at.Sub(u.attached.presentation.View().Status.Session.Started); got != 13*time.Second {
		t.Fatalf("elapsed reset across wake: %v", got)
	}
}

func TestLogicalWaitHumanCancellation(t *testing.T) {
	u, threads, at := logicalWaitUI(t, nil)
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
}

func TestUISnapshotLogicalPausedChildren(t *testing.T) {
	u, threads, at := logicalWaitUI(t, new(int64(1000)))
	if err := threads.shared.Pause(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	u.testActivity(u.shell.Agents).publishActivity()
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source)}
		root, members := threads.shared.Family("main")
		if err := publishNativeFamilySource(api, frame, start.Workspace, root, "/root", "", members); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: "main", Workspace: start.Workspace, Ready: true}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture", Resume: "main"})
	family, _ := hub.Family("main")
	if family == nil || !slices.Contains(family.Threads, "child") {
		t.Fatal("paused child was not retained as a cold runtime destination")
	}
	found := false
	for _, row := range family.Agents.Roster {
		if row.Agent.Thread == "child" {
			found = row.Agent.Paused && !row.Agent.Responding && !row.Agent.Waiting
		}
	}
	if !found {
		t.Fatal("cold family child lost its paused work metadata")
	}
	u.shell.Agents.UpdateRoster(family.Agents)
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

func TestChildCompactionDoesNotReserveAssignment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, threads, _ := logicalWaitUI(t, new(int64(1000)))
		if err := threads.shared.HostCompleted(t.Context(), "child", "child-turn", control.Completed, ""); err != nil {
			t.Fatal(err)
		}
		before, err := threads.shared.Snapshot("child")
		if err != nil {
			t.Fatal(err)
		}
		u.Thread, u.Turn, u.LocalStop, u.Waiting = "child", "", "", ""
		u.attached.threadInput = &threadInput{driver: threads.driver, caller: "/root/worker", admitted: true}
		u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "/compact"}}
		for range 2 {
			if err := flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, t.Context(), false, u.controlObservations, u.review, &u.pendingEditReceipts); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
		}
		after, err := threads.shared.Snapshot("child")
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Effects) != len(before.Effects) || after.Members["child"].Generation != before.Members["child"].Generation || after.Members["child"].Status != control.Completed {
			t.Fatal("native compaction acquired a human assignment or changed its outcome")
		}
		wire := u.Host.Input.(*appServerTestInput)
		appServerOneRequest(t, wire, "thread/compact/start", "")
	})
}

func TestUISnapshotLogicalWaitNoTimeout(t *testing.T) {
	u, threads, at := logicalWaitUI(t, nil)
	u.attached.cwd = "/workspace"
	previousWait := u.attached.session.Agent("/root").Wait
	*at = at.Add(time.Hour)
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	if wake, err := threads.shared.PrepareWake(t.Context(), "main", *at); err != nil || wake != nil {
		t.Fatalf("deadline-free wait woke without an event: %+v %v", wake, err)
	}
	agent := u.attached.session.Agent("/root")
	if previousWait == nil || agent.Wait != previousWait {
		t.Fatal("unchanged wait invalidated roster publication")
	}
	if agent.Responding || !agent.Waiting || agent.WaitingFor != "all" {
		t.Fatalf("stopped parent remained active: %+v", agent)
	}
	rows, _ := u.testMainFrame(100, 10, 0)
	if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Waiting for all") || strings.Contains(text, "timeout") {
		t.Fatalf("deadline-free wait: %s", text)
	}
	assertNativeUISnapshot(t, "logical-wait-no-timeout", rows)
	u.testActivity(u.shell.Agents).publishActivity()
	if text := ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 5, *at, true), "\n")); !strings.Contains(text, "Waiting for all") || strings.Contains(text, "timeout") {
		t.Fatalf("roster differs from deadline-free transcript: %s", text)
	}
}

func TestLogicalStoppedTargetPublishesWakeReason(t *testing.T) {
	for _, status := range []control.Status{control.Paused, control.Failed, control.Completed} {
		t.Run(string(status), func(t *testing.T) {
			u, threads, at := logicalWaitUI(t, nil)
			service := threads.shared
			if status == control.Paused {
				if err := service.Pause(t.Context(), "child"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := service.HostCompleted(t.Context(), "child", "child-turn", status, ""); err != nil {
					t.Fatal(err)
				}
				tree, err := service.Snapshot("child")
				if err != nil {
					t.Fatal(err)
				}
				for _, effect := range tree.Actions() {
					if effect.Kind != "final" || effect.Caller != "child" {
						continue
					}
					if _, err := service.ClaimEffect(t.Context(), "child", effect.ID); err != nil {
						t.Fatal(err)
					}
					if err := service.EffectAcknowledged(t.Context(), "child", effect.ID, control.Receipt{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			wake, err := service.PrepareWake(t.Context(), "main", *at)
			if err != nil || wake == nil {
				t.Fatalf("stopped child did not wake: %+v %v", wake, err)
			}
			if _, err := service.ClaimEffect(t.Context(), "main", wake.ID); err != nil {
				t.Fatal(err)
			}
			if err := service.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "continued"}); err != nil {
				t.Fatal(err)
			}
			syncLogicalWork(&u.attached, &u.Live, threads, *at)
			rows, _ := u.testMainFrame(100, 15, 0)
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Finished waiting for all: worker: "+string(status)) {
				t.Fatalf("stopped-target wake was invisible: %s", text)
			}
		})
	}
}
