package router

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func newActiveWorkTestUI(t *testing.T) (*appServerUI, *time.Time) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return at }
	u.view.clock, u.agents.clock = u.clock, u.clock
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	return u, &at
}

func assertAgentWork(t *testing.T, u *appServerUI, thread string, at time.Time, want time.Duration, running bool) {
	t.Helper()
	agent := u.session.agent(u.session.paths[thread])
	if got := agent.WorkTimer.at(at); got != want {
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
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first", "status": outcome}})
				assertAgentWork(t, u, thread, *at, 19*time.Second, false)
				*at = at.Add(2 * time.Hour)
				appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": thread, "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 100, "outputTokens": 10}}})
				assertAgentWork(t, u, thread, *at, 19*time.Second, false)
				agent := u.session.agent(u.session.paths[thread])
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
	for _, thread := range []string{"main", "child"} {
		for _, timed := range []bool{false, true} {
			name := thread + "/missing"
			if timed {
				name = thread + "/closed"
			}
			t.Run(name, func(t *testing.T) {
				u, at := newActiveWorkTestUI(t)
				turns := []appServerHistoryTurn{
					{ID: "missing", Status: "completed"},
					{ID: "open", Status: "inProgress", StartedAt: 900},
					{ID: "reversed", Status: "interrupted", StartedAt: 800, CompletedAt: 700},
				}
				want := time.Duration(0)
				if timed {
					turns = append(turns,
						appServerHistoryTurn{ID: "completed", Status: "completed", StartedAt: 100, CompletedAt: 120},
						appServerHistoryTurn{ID: "interrupted", Status: "interrupted", StartedAt: 300, CompletedAt: 315})
					want = 35 * time.Second
				}
				if thread == "main" {
					u.restoreMainHistory(turns, nil, nil)
				} else {
					u.restoreActivityThread(appServerThreadInfo{ID: thread, Turns: turns})
				}
				assertAgentWork(t, u, thread, *at, want, false)
				*at = at.Add(24 * time.Hour)
				assertAgentWork(t, u, thread, *at, want, false)
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "live"}})
				*at = at.Add(5 * time.Second)
				assertAgentWork(t, u, thread, *at, want+5*time.Second, true)
			})
		}
	}
}

func TestUISnapshotNativeRosterActiveWork(t *testing.T) {
	u, at := newActiveWorkTestUI(t)
	u.agents.painter.Theme = livediff.DarkTheme
	u.agents.only, u.agents.selected = false, "/root/worker"
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
	}
	*at = at.Add(20 * time.Second)
	rows := []string{"Active: 20 seconds of work"}
	rows = append(rows, u.agents.nativeRoster(100, 6, *at, true)...)
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "idle"}})
	}
	*at = at.Add(10 * time.Minute)
	rows = append(rows, "", "Paused: same work after 10 idle minutes")
	rows = append(rows, u.agents.nativeRoster(100, 6, *at, true)...)
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": thread, "status": map[string]any{"type": "active"}})
	}
	*at = at.Add(7 * time.Second)
	rows = append(rows, "", "Resumed: 27 seconds of work")
	rows = append(rows, u.agents.nativeRoster(100, 6, *at, true)...)
	assertNativeUISnapshot(t, "native-roster-active-work", rows)
}
