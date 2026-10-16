package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotAppServerReasoningBeforeLaterActivity(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, later := range []string{"answer", "command"} {
			t.Run(thread+"_"+later, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				u.view.Conversation = true
				at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
				u.clock = func() time.Time { return at }
				u.view.Clock, u.agents.Clock = u.clock, u.clock
				u.view.Painter.Theme, u.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.agents.setDisplayFilter(false, "/root/worker")
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
				if len(view.Entries) != 2 || view.Entries[0].Source.Kind != "reasoning" || view.Entries[0].Source.Seq >= view.Entries[1].Source.Seq || view.blocks(0)[0].Live {
					t.Fatalf("summary chronology or completion lost: %+v", view.Entries)
				}
				if view.blocks(0)[0].Collapsed || settleActivity(at.Add(time.Second), view) {
					t.Fatal("completed paced summary folded before it could be read")
				}
				uisnapshot.Assert(t, "testdata/snapshots/reasoning-before-"+later+"-"+thread+"-expanded.txt", strings.Join(view.RenderFeed(80, 40).Lines, "\n")+"\n")
				if !settleActivity(at.Add(activityui.OutputDebounce), view) || !view.blocks(0)[0].Collapsed {
					t.Fatal("completed paced summary did not fold after the reading period")
				}
				uisnapshot.Assert(t, "testdata/snapshots/reasoning-before-"+later+"-"+thread+"-collapsed.txt", strings.Join(view.RenderFeed(80, 40).Lines, "\n")+"\n")
			})
		}
	}
}
