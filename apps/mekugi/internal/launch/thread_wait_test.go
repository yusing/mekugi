package launch

import (
	"context"
	json "encoding/json/v2"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/diffrender"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func logicalWaitUI(t *testing.T, timeout *int64) (*appServerUI, *threadExecution, *time.Time) {
	u, threads, at := logicalWaitRunningUI(t, timeout)
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first", "status": "completed"}})
	if err := threads.shared.HostCompleted(t.Context(), "main", "first", control.Completed, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	return u, threads, at
}

func logicalWaitRunningUI(t *testing.T, timeout *int64) (*appServerUI, *threadExecution, *time.Time) {
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
	u.settings.Model, u.settings.Effort = "test-model", "high"
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first"}})
	caller.Call = "wait"
	registration, err := service.RegisterWait(t.Context(), caller, nil, true, "", timeout, *at)
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

func TestUISnapshotUsageLimitWaiting(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	for _, mode := range []string{"true", "false", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			timed := mode == "true"
			u, at := newActiveWorkTestUI(t)
			u.settings.Model, u.settings.Effort = "test-model", "high"
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first"}})
			retained, err := history.NewService(control.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := retained.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "main")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			service := control.New()
			if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: "main", Session: "session", Task: "/root", Turn: "first", Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true}); err != nil {
				t.Fatal(err)
			}
			driver, err := threadbinding.NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			threads := &threadExecution{shared: service, driver: driver}
			*at = at.Add(2 * time.Second)
			u.attached.cwd = "/workspace"
			var deadline *time.Time
			if timed {
				deadline = new(at.Add(time.Hour))
			}
			if mode == "capacity" {
				deadline = new(at.Add(time.Second))
			}
			wait := threads.shared.WaitForUsageLimit
			if mode == "capacity" {
				wait = threads.shared.WaitForCapacity
			}
			if _, err := wait(t.Context(), "main", "first", deadline); err != nil {
				t.Fatal(err)
			}
			if err := threads.shared.HostCompleted(t.Context(), "main", "first", control.Completed, control.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			syncLogicalWork(&u.attached, &u.Live, threads, *at)
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first", "status": "completed"}})
			rows, _ := u.testMainFrame(80, 8, 0)
			view := u.attached.presentation.View()
			if !view.InterruptAvailable || !view.Status.Session.Running || view.Status.Feedback.Alert || view.Status.Session.CompletionKnown {
				t.Fatalf("lost virtual wait: %+v", view)
			}
			assertNativeUISnapshot(t, "usage-limit-waiting-"+mode, rows)
			u.testActivity(u.shell.Agents).publishActivity()
			assertNativeUISnapshot(t, "usage-limit-roster-"+mode, u.shell.Agents.NativeRoster(80, 5, *at, true))
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			if deadline != nil {
				wake, err := service.PrepareWake(t.Context(), "main", *deadline)
				check(err)
				_, err = service.ClaimEffect(t.Context(), "main", wake.ID)
				check(err)
				check(service.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "retry"}))
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "retry"}})
				syncLogicalWork(&u.attached, &u.Live, threads, *at)
				u.testMainFrame(100, 8, 0)
				if strings.Contains(u.attached.presentation.View().Status.Session.Text, "retry at") {
					t.Fatal("resumed request retained old retry deadline")
				}
				deadline = new(deadline.Add(time.Minute))
				_, err = wait(t.Context(), "main", "retry", deadline)
				check(err)
				check(service.HostCompleted(t.Context(), "main", "retry", control.Completed, control.HostFailure{}))
				syncLogicalWork(&u.attached, &u.Live, threads, *at)
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "retry", "status": "completed"}})
				u.testMainFrame(100, 8, 0)
				if !strings.Contains(u.attached.presentation.View().Status.Session.Text, deadline.Format("15:04:05")) {
					t.Fatal("repeated rejection hid the new deadline")
				}
			}
			member, _ := service.Member("main")
			_, err = service.HumanInterrupt(t.Context(), "main", member.Stop.Wait)
			check(err)
			syncLogicalWork(&u.attached, &u.Live, threads, *at)
			u.testMainFrame(100, 8, 0)
			if u.attached.presentation.View().Status.Session.Running {
				t.Fatal("interrupted provider wait still appeared running")
			}
		})
	}
}

func TestUISnapshotNativeRetryProgress(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u, at := newActiveWorkTestUI(t)
			u.attached.cwd = "/workspace"
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.shell.Agents.Painter.Theme = diffrender.DarkTheme
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
			notify := func(turn, message string, retry bool) {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": retry,
					"error": map[string]any{"message": message, "additionalDetails": "Server overloaded", "codexErrorInfo": "serverOverloaded"}})
			}
			for _, message := range []string{"Reconnecting... 1/5", "Reconnecting... 2/5"} {
				notify("first", message, true)
				rows, _ := u.testMainFrame(100, 8, 0)
				view := u.attached.presentation.View()
				if thread == "main" && (!strings.Contains(view.Status.Composer.Text, message) || view.Status.Composer.Alert || !view.InterruptAvailable) {
					t.Fatalf("retry lost from shared status: %+v", view.Status)
				}
				if got := u.attached.session.RetryStatus(thread); !strings.Contains(got, message) || !strings.Contains(got, "Server overloaded") {
					t.Fatalf("retry facts lost: %q", got)
				}
				if message == "Reconnecting... 2/5" {
					if thread == "main" {
						assertNativeUISnapshot(t, "native-retry-status", rows)
					}
					u.testActivity(u.shell.Agents).publishActivity()
					assertNativeUISnapshot(t, "native-retry-roster-"+thread, u.shell.Agents.NativeRoster(100, 6, *at, true))
				}
			}
			notify("old", "stale retry", true)
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "old", "status": "completed"}})
			if !strings.Contains(u.attached.session.RetryStatus(thread), "2/5") {
				t.Fatal("stale turn changed current retry")
			}
			appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": "first", "itemId": "answer", "delta": "Recovered"})
			if u.attached.session.RetryStatus(thread) != "" {
				t.Fatal("response did not clear retry")
			}
			for _, outcome := range []string{"completed", "interrupted", "failed"} {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": outcome}})
				notify(outcome, "Reconnecting... 1/5", true)
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": outcome, "status": outcome}})
				notify(outcome, "late retry", true)
				if u.attached.session.RetryStatus(thread) != "" {
					t.Fatalf("%s retained or reopened retry", outcome)
				}
			}
			notify("first", "terminal error", false)
			if u.attached.session.RetryStatus(thread) != "" {
				t.Fatal("terminal error became retry progress")
			}
		})
	}
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

func TestSupersededLocalWaitRestoresWorkingRoster(t *testing.T) {
	u, threads, at := logicalWaitRunningUI(t, nil)
	if err := threads.shared.Execution(t.Context(), "main", "first", "exec", "", false); err != nil {
		t.Fatal(err)
	}
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	agent := u.attached.session.Agent("/root")
	if agent.Waiting || agent.Wait != nil || agent.WaitingFor != "" || !agent.Responding || u.LocalStop != "" {
		t.Fatalf("ordinary work retained a stale wait: %+v", agent)
	}
	u.testActivity(u.shell.Agents).publishActivity()
	if text := ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 5, *at, true), "\n")); strings.Contains(text, "Waiting for") {
		t.Fatalf("working roster displays stale wait: %s", text)
	}
	// Each owner-loop pass observes the running host again. Unchanged work
	// must reuse the roster publication, whose identity versions roster pages.
	published := u.attached.childSource.Publication(u.attached.presentation, u.attached.source)
	*at = at.Add(time.Second)
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	if u.attached.childSource.Publication(u.attached.presentation, u.attached.source) != published {
		t.Fatal("unchanged running work republished the roster")
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
			if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
		}
		appServerOneRequest(t, wire, "turn/start", "queued during recovery")
	})
}

func TestLogicalWaitKeepsQueuedComposerInput(t *testing.T) {
	u, threads, at := logicalWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "next assignment\t")
	member, _ := threads.shared.Member("main")
	if wire.Len() != 0 || member.Wait.State != "registered" || len(u.attached.conversation.Queued) != 1 || u.attached.threadInput.preparing() {
		t.Fatalf("queued composer input superseded wait: wire=%s unsent=%d queued=%d preparing=%v member=%+v", wire.String(), len(u.attached.conversation.Unsent), len(u.attached.conversation.Queued), u.attached.threadInput.preparing(), member)
	}
	if next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, false); len(next.Batch.Parts) != 0 || len(u.attached.conversation.Queued) != 1 {
		t.Fatal("ordinary input flushing drained the queue during a logical wait")
	}
	// Addressed delegate messages still wake the parent. Its queued composer
	// input remains queued for the resumed turn's completion.
	message, err := threads.shared.PrepareAction(t.Context(), control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "message", Item: "exec"}, "/root", "message", "ready")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.shared.ClaimEffect(t.Context(), "child", message.ID); err != nil {
		t.Fatal(err)
	}
	if err := threads.shared.EffectAcknowledged(t.Context(), "child", message.ID, control.Receipt{}); err != nil {
		t.Fatal(err)
	}
	wake, err := threads.shared.PrepareWake(t.Context(), "main", *at)
	if err != nil || wake == nil {
		t.Fatalf("addressed message lost wake: %+v %v", wake, err)
	}
	if _, err := threads.shared.ClaimEffect(t.Context(), "main", wake.ID); err != nil {
		t.Fatal(err)
	}
	if err := threads.shared.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "resumed"}); err != nil {
		t.Fatal(err)
	}
	appServerTestTurn(t, u, "resumed")
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	if next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, false); len(next.Batch.Parts) != 0 || len(u.attached.conversation.Queued) != 1 {
		t.Fatal("wake promoted queued composer input into a steer")
	}
	u.Live.Completed(true)
	if next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, false); len(next.Batch.Parts) != 1 || next.Batch.Parts[0].Text != "next assignment" || next.Steer {
		t.Fatalf("queue did not release after resumed turn: %+v", next)
	}
}

func TestChildCompactionDoesNotReserveAssignment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, threads, _ := logicalWaitUI(t, new(int64(1000)))
		if err := threads.shared.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
			t.Fatal(err)
		}
		before, err := threads.shared.Snapshot("child")
		if err != nil {
			t.Fatal(err)
		}
		u.Thread, u.Turn, u.LocalStop, u.Waiting = "child", "", "", ""
		u.attached.threadInput = &threadInput{driver: threads.driver, caller: "/root/worker", admitted: true}
		u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "/compact", Compact: new(conversationmodel.CompactIntent{})}}
		for range 2 {
			if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
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

func TestLogicalWaitPublicationUsesSelectedWait(t *testing.T) {
	finished := func(id, stop string) *control.Wait {
		return &control.Wait{
			Effect: control.Effect{ID: id, Turn: id + "-turn", State: "completed"}, Stop: stop,
			Terminal: &control.Event{Kind: "message", Task: "/root/worker"},
		}
	}
	for _, tc := range []struct {
		name              string
		current, recovery *control.Wait
		want              *control.Wait
	}{
		{name: "missing"},
		{name: "current", current: finished("current", "committed"), want: finished("current", "committed")},
		{name: "recovery without current", recovery: finished("recovery", "committed"), want: finished("recovery", "committed")},
		{name: "recovery overrides current", current: finished("current", ""), recovery: finished("recovery", "committed"), want: finished("recovery", "committed")},
		{name: "recovery does not borrow current stop", current: finished("current", "committed"), recovery: finished("recovery", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retained, err := history.NewService(control.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := retained.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "main")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			service := control.New()
			if err := service.AdmitRoot(t.Context(), session, &control.Member{
				Thread: "main", Session: "session", Task: "/root", Status: control.Working,
				AudienceAdmitted: true, Wait: tc.current, RecoveryWait: tc.recovery,
			}); err != nil {
				t.Fatal(err)
			}
			driver, err := threadbinding.NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			u, at := newActiveWorkTestUI(t)
			syncLogicalWork(&u.attached, &u.Live, &threadExecution{shared: service, driver: driver}, *at)
			var finishedItems []*sessionpresentation.NativeItem
			for _, entry := range u.attached.session.Activity().Main.Entries {
				if native := entry.Source.Native; native != nil && native.Wait != nil {
					finishedItems = append(finishedItems, native)
				}
			}
			if tc.want == nil {
				if len(finishedItems) != 0 {
					t.Fatalf("unexpected finished wait: %+v", finishedItems)
				}
				return
			}
			if len(finishedItems) != 1 {
				t.Fatalf("finished wait count = %d, want 1", len(finishedItems))
			}
			got := finishedItems[0]
			if got.Item != "threadcontrol:wait:"+tc.want.ID || got.Turn != tc.want.Turn || got.Wait.WaitReason != "message received" {
				t.Fatalf("wrong selected wait identity or reason: %+v, wait: %+v", got, got.Wait)
			}
		})
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
				if err := service.HostCompleted(t.Context(), "child", "child-turn", status, control.HostFailure{}); err != nil {
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
			want := "Finished waiting for worker: " + string(status)
			if status != control.Completed {
				want += ": completed"
			}
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, want) || strings.Contains(text, "completed: completed") {
				t.Fatalf("stopped-target wake was invisible: %s", text)
			}
		})
	}
}

func TestUISnapshotLogicalWaitAllOutcome(t *testing.T) {
	for _, reason := range []string{"paused", "delivery failed"} {
		t.Run(reason, func(t *testing.T) {
			u, threads, at := logicalWaitUI(t, nil)
			u.attached.cwd = "/workspace"
			service := threads.shared
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.HumanInput(t.Context(), "main", "next")
			check(err)
			caller := control.Identity{Thread: "main", Session: "session", Turn: "next", Call: "wait-all", Item: "exec"}
			check(service.RequestStarted(t.Context(), caller, true))
			_, err = service.RegisterWait(t.Context(), caller, nil, true, control.WaitAll, nil, *at)
			check(err)
			check(service.Execution(t.Context(), "main", "next", "exec", "", true))
			_, err = service.ClaimLocalStop(t.Context(), "main", "next", "exec")
			check(err)
			check(service.HostCompleted(t.Context(), "main", "next", control.Completed, control.HostFailure{}))
			if reason == "paused" {
				check(service.Pause(t.Context(), "child"))
			} else {
				check(service.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}))
				tree, err := service.Snapshot("child")
				check(err)
				for _, effect := range tree.Effects {
					if effect.Kind == "final" && effect.Caller == "child" {
						_, err := service.ClaimEffect(t.Context(), "child", effect.ID)
						check(err)
						check(service.EffectAcknowledged(t.Context(), "child", effect.ID, control.Receipt{Error: "injection rejected"}))
					}
				}
			}
			wake, err := service.PrepareWake(t.Context(), "main", *at)
			check(err)
			if wake == nil {
				t.Fatal("all-wait did not wake")
			}
			_, err = service.ClaimEffect(t.Context(), "main", wake.ID)
			check(err)
			check(service.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "continued"}))
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
			syncLogicalWork(&u.attached, &u.Live, threads, *at)
			rows, _ := u.testMainFrame(80, 10, 0)
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Finished waiting for worker: "+reason+": completed") {
				t.Fatalf("aggregate wake was invisible: %s", text)
			}
			snapshot := "logical-wait-all-outcome"
			if reason == "delivery failed" {
				snapshot += "-delivery-failed"
			}
			assertNativeUISnapshot(t, snapshot, rows)
		})
	}
}

func TestStandaloneCompactionOmitsAgentWaitPresentation(t *testing.T) {
	u, threads, at := logicalWaitUI(t, nil)
	s := threads.shared
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	caller := control.Identity{Thread: "main", Session: "session", Turn: "checkpoint-turn", Call: "compact", Item: "compact-exec"}
	check(s.RequestStarted(ctx, caller, true))
	_, err := s.RegisterCompaction(ctx, caller, nil, false, "", nil, *at, true, "")
	check(err)
	check(s.Execution(ctx, caller.Thread, caller.Turn, caller.Item, "", true))
	_, err = s.ClaimLocalStop(ctx, caller.Thread, caller.Turn, caller.Item)
	check(err)
	check(s.HostCompleted(ctx, caller.Thread, caller.Turn, control.Completed, control.HostFailure{}))
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	if u.attached.session.Lifecycle("main").Wait != nil {
		t.Fatal("standalone compact published a targetless agent wait")
	}
	compact, err := s.ClaimEffect(ctx, caller.Thread, caller.Call+":compact")
	check(err)
	check(s.EffectAcknowledged(ctx, caller.Thread, compact.ID, control.Receipt{}))
	wake, err := s.PrepareWake(ctx, caller.Thread, *at)
	check(err)
	_, err = s.ClaimEffect(ctx, caller.Thread, wake.ID)
	check(err)
	check(s.EffectAcknowledged(ctx, caller.Thread, wake.ID, control.Receipt{Turn: "continued"}))
	syncLogicalWork(&u.attached, &u.Live, threads, *at)
	if u.attached.threadInput.waitID == caller.Call || u.attached.session.Lifecycle("main").Wait != nil {
		t.Fatal("standalone continuation published an agent-wait outcome")
	}
}
