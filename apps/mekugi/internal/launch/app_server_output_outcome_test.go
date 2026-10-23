package launch

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
			u.shell.Transcript.Conversation = true
			u.testActivity(u.shell.Transcript).testAudience()
			item := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "failed", AggregatedOutput: new("interrupted output\n")}
			if restored {
				u.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}})
			} else {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			}
			awaitMain(t, u, "interrupted output")
		})
	}
}
