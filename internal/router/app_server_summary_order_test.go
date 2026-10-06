package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotAppServerReasoningBeforeLaterActivity(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, later := range []string{"answer", "command"} {
			t.Run(thread+"_"+later, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				u.view.conversation = true
				at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
				u.clock = func() time.Time { return at }
				u.view.clock, u.agents.clock = u.clock, u.clock
				u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.agents.only, u.agents.selected = false, "/root/worker"
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				notify := func(method string, params map[string]any) {
					t.Helper()
					params["threadId"], params["turnId"] = thread, "t"
					appServerTestNotify(t, u, method, params)
				}
				notify("item/started", map[string]any{"item": map[string]any{"id": "r", "type": "reasoning"}})
				notify("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "delta": "Inspecting order\n\nFirst checkpoint.\nSecond checkpoint.\n"})
				at = at.Add(2 * time.Second)
				notify("item/completed", map[string]any{"item": map[string]any{"id": "r", "type": "reasoning"}})
				// No paint ticks between summary arrival, completion and later activity.
				if later == "answer" {
					notify("item/completed", map[string]any{"item": map[string]any{"id": "a", "type": "agentMessage", "phase": "final_answer", "text": "Answer after reasoning."}})
				} else {
					notify("item/started", map[string]any{"item": map[string]any{"id": "c", "type": "commandExecution", "command": "echo later"}})
					notify("item/completed", map[string]any{"item": map[string]any{"id": "c", "type": "commandExecution", "command": "echo later", "exitCode": 0, "durationMs": 100}})
				}
				rollCommandOutput(u)
				if len(view.entries) != 2 || view.entries[0].Kind != "reasoning" || view.entries[0].Seq >= view.entries[1].Seq || view.entries[0].blocks[0].Live {
					t.Fatalf("summary chronology or completion lost: %+v", view.entries)
				}
				if !view.entries[0].blocks[0].Collapsed {
					t.Fatal("completed paced summary did not fold immediately")
				}
				uisnapshot.Assert(t, "testdata/snapshots/reasoning-before-"+later+"-"+thread+"-collapsed.txt", strings.Join(view.renderFeed(80, 40).lines, "\n")+"\n")
			})
		}
	}
}
