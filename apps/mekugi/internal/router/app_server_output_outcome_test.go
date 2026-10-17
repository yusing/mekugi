package router

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestAppServerUnknownExitKeepsOutput(t *testing.T) {
	for _, restored := range []bool{false, true} {
		name := "live"
		if restored {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.Conversation = true
			item := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "failed", AggregatedOutput: new("interrupted output\n")}
			if restored {
				var entries []activityPaneEntry
				u.restoreActivityItem(binding.ThreadInfo{ID: "main", Cwd: u.session.cwd}, binding.HistoryTurn{ID: "turn"}, item, "/root", u.now(), &entries, nil)
				u.applyActivity(entries, nil)
			} else {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			}
			awaitMain(t, u, "interrupted output")
		})
	}
}
