package launch

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestUISnapshotNativeWaitActiveWork(t *testing.T) {
	u, _, at := nativeRunningUI(t)
	u.attached.cwd = "/workspace"
	u.shell.Agents.Painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn"}})
	wait := func(method, turn, item string) {
		status := "inProgress"
		if method == "item/completed" {
			status = "completed"
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": turn, "item": map[string]any{
			"id": item, "type": "collabAgentToolCall", "tool": "wait", "status": status, "receiverThreadIds": []string{"child"},
		}})
	}
	var rows []string
	capture := func(phase string, work time.Duration, running bool, composer string) {
		t.Helper()
		assertAgentWork(t, u, "main", *at, work, running)
		frame, _ := u.testMainFrame(96, 9, 0)
		if !strings.Contains(ansi.Strip(strings.Join(frame, "\n")), composer) {
			t.Fatalf("%s: expected %q in rendered composer:\n%s", phase, composer, strings.Join(frame, "\n"))
		}
		nativeDuration := jsonv1.FormatDurationAsNano(true)
		wire, err := json.Marshal(sessionapi.NativeFrame{View: u.attached.presentation.View()}, nativeDuration)
		if err != nil {
			t.Fatal(err)
		}
		var detached sessionapi.NativeFrame
		if err := json.Unmarshal(wire, &detached, nativeDuration); err != nil {
			t.Fatal(err)
		}
		if got := paintedComposerStatus(detached.View, &u.shell.Transcript.Painter, *at); !strings.Contains(ansi.Strip(got), composer) {
			t.Fatalf("%s: detached composer = %q, want %q", phase, ansi.Strip(got), composer)
		}
		u.testActivity(u.shell.Agents).publishActivity()
		rows = append(rows, phase)
		rows = append(rows, frame...)
		rows = append(rows, u.shell.Agents.RosterSheet(96, 6, *at)...)
		rows = append(rows, "")
	}
	*at = at.Add(12 * time.Second)
	capture("Before waiting", 12*time.Second, true, "Working 12s")
	wait("item/started", "first", "one")
	*at = at.Add(20 * time.Second)
	wait("item/started", "first", "one") // Repeated starts must not move the anchor.
	wait("item/started", "first", "two")
	appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "main", "status": map[string]any{"type": "active"}})
	wait("item/completed", "first", "one")
	*at = at.Add(40 * time.Second)
	assertAgentWork(t, u, "child", *at, 72*time.Second, true)
	if !u.Busy(false) || !u.attached.presentation.View().InterruptAvailable {
		t.Fatal("pausing the clock made the pending native turn idle")
	}
	capture("Waiting: 60 seconds later; a second wait is still pending", 12*time.Second, false, "Working 12s")
	wait("item/completed", "first", "two")
	*at = at.Add(7 * time.Second)
	wait("item/completed", "first", "two")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "first", "itemId": "answer", "delta": "Back to work."})
	capture("After waiting: 7 seconds of resumed work", 19*time.Second, true, "Working 19s")
	appServerTestTurnEnd(t, u, "first", "completed")
	*at = at.Add(5 * time.Second)
	capture("Completed", 19*time.Second, false, "Completed in 19s")
	assertNativeUISnapshot(t, "native-wait-active-work", rows)
}

func TestNativeWaitActiveWorkInterruption(t *testing.T) {
	u, _, at := nativeWaitUI(t, nil)
	*at = at.Add(time.Minute)
	appServerTestTurnEnd(t, u, "first", "interrupted")
	*at = at.Add(time.Minute)
	nativeWaitItemFixture(t, u, "item/completed", nil)
	assertAgentWork(t, u, "main", *at, 2*time.Second, false)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "second"}})
	nativeWaitItemFixture(t, u, "item/started", nil)
	*at = at.Add(5 * time.Second)
	assertAgentWork(t, u, "main", *at, 7*time.Second, true)
}

func TestNativeDelegateWaitPausesOnlyItsOwnWork(t *testing.T) {
	u, _, at := nativeRunningUI(t)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn"}})
	wait := func(method, status string) {
		appServerTestNotify(t, u, method, map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{
			"id": "delegate-wait", "type": "collabAgentToolCall", "tool": "wait", "status": status, "receiverThreadIds": []string{"grandchild"},
		}})
	}
	*at = at.Add(3 * time.Second)
	wait("item/started", "inProgress")
	*at = at.Add(time.Minute)
	assertAgentWork(t, u, "child", *at, 3*time.Second, false)
	assertAgentWork(t, u, "main", *at, 63*time.Second, true)
	wait("item/completed", "completed")
	*at = at.Add(4 * time.Second)
	assertAgentWork(t, u, "child", *at, 7*time.Second, true)
}

func newActiveWorkTestUI(t *testing.T) (*appServerUI, *time.Time) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return at }
	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	return u, &at
}

func assertAgentWork(t *testing.T, u *appServerUI, thread string, at time.Time, want time.Duration, running bool) {
	t.Helper()
	agent := u.attached.session.Agent(u.attached.session.KnownCaller(thread))
	if got := agent.WorkTimer.At(at); got != want {
		t.Fatalf("%s active work = %s, want %s", thread, got, want)
	}
	if got := !agent.WorkTimer.Since.IsZero(); got != running {
		t.Fatalf("%s timer running = %t, want %t: %+v", thread, got, running, agent.WorkTimer)
	}
}

func TestAppServerActiveWorkPausesAndResumes(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, inactive := range []string{"idle", "notLoaded", "systemError"} {
			t.Run(thread+"/"+inactive, func(t *testing.T) {
				u, at := newActiveWorkTestUI(t)
				status := func(state string) {
					appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": state}})
				}
				status("active")
				assertAgentWork(t, u, thread, *at, 0, false)
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
				*at = at.Add(12 * time.Second)
				status("active") // Repeated status must not reset the active interval.
				*at = at.Add(8 * time.Second)
				status(inactive)
				assertAgentWork(t, u, thread, *at, 20*time.Second, false)
				*at = at.Add(time.Hour)
				status(inactive)
				assertAgentWork(t, u, thread, *at, 20*time.Second, false)
				status("active")
				*at = at.Add(7 * time.Second)
				assertAgentWork(t, u, thread, *at, 27*time.Second, true)
				// Another thread's lifecycle must not stop this agent's timer.
				other := "main"
				if thread == "main" {
					other = "child"
				}
				appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": other, "status": map[string]any{"type": "idle"}})
				assertAgentWork(t, u, thread, *at, 27*time.Second, true)
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first", "status": "completed"}})
				*at = at.Add(time.Hour)
				assertAgentWork(t, u, thread, *at, 27*time.Second, false)
			})
		}
	}
}

func TestAppServerActiveWorkTurnEndsAndLateUsage(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, outcome := range []string{"completed", "interrupted", "failed"} {
			t.Run(thread+"/"+outcome, func(t *testing.T) {
				u, at := newActiveWorkTestUI(t)
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
				*at = at.Add(19 * time.Second)
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "old", "status": "completed"}})
				assertAgentWork(t, u, thread, *at, 19*time.Second, true)
				agent := u.attached.session.Agent(u.attached.session.KnownCaller(thread))
				if !agent.Responding || agent.Final {
					t.Fatalf("old completion changed current roster state: %+v", agent)
				}
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first", "status": outcome}})
				assertAgentWork(t, u, thread, *at, 19*time.Second, false)
				if agent.Responding || agent.Final != (outcome == "completed") {
					t.Fatalf("current completion did not settle roster state: %+v", agent)
				}
				*at = at.Add(2 * time.Hour)
				appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": thread, "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 100, "outputTokens": 10}}})
				assertAgentWork(t, u, thread, *at, 19*time.Second, false)
				if !agent.LastResponse.Equal(*at) {
					t.Fatal("late usage did not update the independent response-age clock")
				}
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "second"}})
				*at = at.Add(11 * time.Second)
				assertAgentWork(t, u, thread, *at, 30*time.Second, true)
			})
		}
	}
}

func TestAppServerActiveWorkRestoresClosedSpans(t *testing.T) {
	const thread = "main"
	for _, timed := range []bool{false, true} {
		name := thread + "/missing"
		if timed {
			name = thread + "/closed"
		}
		t.Run(name, func(t *testing.T) {
			u, at := newActiveWorkTestUI(t)
			turns := []binding.HistoryTurn{
				{ID: "missing", Status: "completed"},
				{ID: "open", Status: "inProgress", StartedAt: 900},
				{ID: "reversed", Status: "interrupted", StartedAt: 800, CompletedAt: 700},
			}
			want := time.Duration(0)
			if timed {
				turns = append(turns,
					binding.HistoryTurn{ID: "completed", Status: "completed", StartedAt: 100, CompletedAt: 120},
					binding.HistoryTurn{ID: "interrupted", Status: "interrupted", StartedAt: 300, CompletedAt: 315})
				want = 35 * time.Second
			}
			u.restoreHistory(turns)
			assertAgentWork(t, u, thread, *at, want, false)
			*at = at.Add(24 * time.Hour)
			assertAgentWork(t, u, thread, *at, want, false)
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "live"}})
			*at = at.Add(5 * time.Second)
			assertAgentWork(t, u, thread, *at, want+5*time.Second, true)
		})
	}
}

func TestUISnapshotNativeRosterActiveWork(t *testing.T) {
	u, at := newActiveWorkTestUI(t)
	u.shell.Agents.Painter.Theme = diffrender.DarkTheme
	u.testActivity(u.shell.Agents).setDisplayFilter(false, "/root/worker")
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
	}
	*at = at.Add(20 * time.Second)
	rows := []string{"Active: 20 seconds of work"}
	rows = append(rows, u.testActivity(u.shell.Agents).RosterSheet(100, 6, *at)...)
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "idle"}})
	}
	*at = at.Add(10 * time.Minute)
	rows = append(rows, "", "Paused: same work after 10 idle minutes")
	rows = append(rows, u.testActivity(u.shell.Agents).RosterSheet(100, 6, *at)...)
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "active"}})
	}
	*at = at.Add(7 * time.Second)
	rows = append(rows, "", "Resumed: 27 seconds of work")
	rows = append(rows, u.testActivity(u.shell.Agents).RosterSheet(100, 6, *at)...)
	assertNativeUISnapshot(t, "native-roster-active-work", rows)
}
