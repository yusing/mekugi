package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerRosterReasoningTitlePreservesTranscript(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
			view, agent := u.view, activityPaneAgent{Name: "/root", Responding: true}
			if thread == "child" {
				view, agent.Name = u.agents, "/root/worker"
			}
			var full strings.Builder
			for _, delta := range []string{"**Checking routing**", "\n\n" + strings.Repeat("Public detail. ", 30), "\n\nFinal public observation."} {
				full.WriteString(delta)
				reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": delta})
				if got, _ := u.agents.testCurrent(agent, time.Now()); ansi.Strip(got) != "Checking routing" {
					t.Fatalf("streaming body flooded roster status: %q", ansi.Strip(got))
				}
				if len(view.Entries) != 1 || view.Entries[0].Source.Text != full.String() {
					t.Fatal("roster title replaced the retained reasoning")
				}
			}
			reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning"}})
			if got, _ := u.agents.testCurrent(agent, time.Now()); ansi.Strip(got) != "Checking routing" {
				t.Fatalf("completion lost reasoning title: %q", ansi.Strip(got))
			}
			if page := view.Painter.DialogPage(view.blocks(0)[0], 80); page.Text != full.String() {
				t.Fatalf("completed dialog lost full reasoning: %q", page.Text)
			}
			reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "mcat next.go"}})
			if got, _ := u.agents.testCurrent(agent, time.Now()); ansi.Strip(got) != "Read next.go" {
				t.Fatalf("reasoning title obscured newer operation: %q", ansi.Strip(got))
			}
			if view.Entries[0].Source.Text != full.String() {
				t.Fatal("later operation discarded reasoning")
			}
		})
	}
}

func TestUISnapshotNativeRosterReasoningTitles(t *testing.T) {
	for _, width := range []int{48, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
			u.clock = func() time.Time { return at }
			u.view.Clock, u.agents.Clock = u.clock, u.clock
			u.view.Painter.Theme, u.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
			reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker", "agentRole": "worker"}})
			for _, thread := range []string{"main", "child"} {
				reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
				body := "**Checking " + thread + "**\n\n" + strings.Repeat("Long reasoning body should stay in transcript. ", 20)
				reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": body})
			}
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-reasoning-%d", width), u.agents.NativeRoster(width, 6, at, true))
			for _, thread := range []string{"main", "child"} {
				reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "plain", "delta": strings.Repeat("Checking the implementation. ", 100)})
			}
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-reasoning-paragraph-live-%d", width), u.agents.NativeRoster(width, 6, at, true))
			for _, thread := range []string{"main", "child"} {
				reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "plain", "type": "reasoning"}})
			}
			assertNativeUISnapshot(t, fmt.Sprintf("native-roster-reasoning-paragraph-done-%d", width), u.agents.NativeRoster(width, 6, at, true))
		})
	}
}
