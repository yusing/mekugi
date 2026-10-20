package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotNativeMainRepliesStayInTranscript(t *testing.T) {
	for _, width := range []int{36, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return now }
			u.turn, u.status, u.model = "active", "Working", "snapshot-model"
			u.view.painter.Theme = livediff.DarkTheme
			entries := []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Main reply stays in the transcript.", Observed: now}}
			for i := range 20 {
				entries = append(entries, activityPaneEntry{Seq: uint64(i + 2), Agent: "Main", Kind: "tool", Text: fmt.Sprintf("Ran `echo activity-%02d`", i+1), Observed: now})
			}
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: entries})
			rows, _ := u.mainFrame(width, 16, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("native-main-transcript-following-%d", width), rows)
			u.view.following, u.view.offset = false, 0
			rows, _ = u.mainFrame(width, 16, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("native-main-transcript-scrolled-%d", width), rows)
		})
	}
}

func TestNativeMainTranscriptScrollingAndTurnEnd(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "reply", "type": "agentMessage", "text": "Original Main reply"}})
			for i := range 20 {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": fmt.Sprint(i), "type": "commandExecution", "command": fmt.Sprint("echo activity-", i)}})
			}
			u.mainFrame(80, 24, 0)
			if u.view.feedTop != 1 {
				t.Fatal("transcript lost rows to a reply copy")
			}
			u.view.following, u.view.offset = false, 0
			frame, _ := u.mainFrame(80, 24, 0)
			if strings.Count(ansi.Strip(strings.Join(frame, "\n")), "Original Main reply") != 1 {
				t.Fatal("original reply is not accessible exactly once when scrolling up")
			}
			for i := 0; !u.view.following && i < u.view.feedLines; i++ {
				u.view.scrollKey('j')
				u.mainFrame(80, 24, 0)
			}
			if !u.view.following {
				t.Fatal("incremental scrolling did not reach the bottom")
			}
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": status}})
			u.mainFrame(80, 24, 0)
			if u.view.feedTop != 1 || u.turn != "" {
				t.Fatal("turn end changed transcript geometry or retained an active turn")
			}
		})
	}
}
