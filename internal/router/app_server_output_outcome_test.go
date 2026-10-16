package router

import "testing"

func TestAppServerUnknownExitKeepsOutput(t *testing.T) {
	for _, restored := range []bool{false, true} {
		name := "live"
		if restored {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.conversation = true
			item := appServerItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "failed", AggregatedOutput: new("interrupted output\n")}
			if restored {
				var entries []activityPaneEntry
				lastMessage := -1
				u.restoreActivityItem(appServerThreadInfo{ID: "main", Cwd: u.session.cwd}, appServerHistoryTurn{ID: "turn"}, item, "/root", u.now(), &entries, &lastMessage)
				u.applyActivity(entries, nil)
			} else {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			}
			awaitMain(t, u, "interrupted output")
		})
	}
}
