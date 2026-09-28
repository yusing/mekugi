package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeMainReplyPinLifecycle(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
			send := func(id, text string) {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": id, "type": "agentMessage", "text": text}})
			}
			send("reply", "Latest Main response")
			for i := range 20 {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": fmt.Sprint(i), "type": "commandExecution", "command": fmt.Sprint("echo activity-", i)}})
			}
			check := func(want string) {
				t.Helper()
				frame, _ := u.mainFrame(80, 24, 0)
				if len(frame) != 24 || u.view.feedTop <= 1 || !strings.Contains(ansi.Strip(frame[1]), want) {
					t.Fatalf("reply not pinned above activity: top=%d\n%s", u.view.feedTop, strings.Join(frame, "\n"))
				}
				if u.view.feedRows < 12 {
					t.Fatal("pin crowded out the transcript")
				}
			}
			check("Latest Main response")
			u.view.following, u.view.offset = false, 0
			frame, _ := u.mainFrame(80, 24, 0)
			if u.view.feedTop != 1 || strings.Count(ansi.Strip(strings.Join(frame, "\n")), "Latest Main response") != 1 {
				t.Fatal("visible original was duplicated by a pin")
			}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "new", "type": "agentMessage", "text": "New response"}})
			check("New response")
			appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"main","turnId":"t","itemId":"new","delta":" updated"}}`)
			check("New response updated")
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"t","status":"completed"}}}`)
			check("New response updated")
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": status}})
			u.mainFrame(80, 24, 0)
			if u.view.pinMainReply || u.view.feedTop != 1 {
				t.Fatal("Main turn end retained the pin")
			}
		})
	}
}

func TestNativeMainReplyPinBoundsAndSelection(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.turn = "t"
	u.view.applyAppServerItem("main", "main", "t", "reply", "item/completed", "", appServerItem{Type: "agentMessage", Text: strings.Repeat("Long reply content.\n", 40)})
	for i := range 30 {
		u.view.applyAppServerItem("main", "main", "t", fmt.Sprint(i), "item/completed", "", appServerItem{Type: "commandExecution", Command: fmt.Sprint("echo activity-", i)})
	}
	for _, size := range [][2]int{{80, 24}, {24, 12}, {12, 6}} {
		frame, _ := u.mainFrame(size[0], size[1], 0)
		if len(frame) != size[1] {
			t.Fatalf("frame height = %d, want %d", len(frame), size[1])
		}
		for _, line := range frame {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("row exceeds width %d: %q", size[0], line)
			}
		}
		if size[1] == 6 && u.view.feedTop != 1 {
			t.Fatal("tiny pane retained pin")
		}
		if size[1] == 24 && !strings.Contains(frame[u.view.feedTop-3], "…") {
			t.Fatal("long pin has no clipping marker")
		}
	}
	shell := selectionTestUI("main pinned", "Reply body", "separator", "transcript")
	shell.main.view.conversation, shell.main.view.pinMainReply = true, true
	shell.main.view.feedTop, shell.main.view.feedRows = 4, 1
	selectionTestDrag(t, shell, 0, 1, 4, 1)
	if got := shell.selection.text(); got != "Reply" {
		t.Fatalf("pinned selection = %q", got)
	}
}

func TestNativeMainReplyPinViewportTransitions(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.turn = "t"
	for i := range 10 {
		u.view.applyAppServerItem("main", "main", "t", fmt.Sprint(i), "item/completed", "", appServerItem{Type: "commandExecution", Command: fmt.Sprint("echo before-", i)})
	}
	u.view.applyAppServerItem("main", "main", "t", "reply", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Unique reply body.\nSecond reply line."})
	for i := range 30 {
		u.view.applyAppServerItem("main", "main", "t", fmt.Sprint("after-", i), "item/completed", "", appServerItem{Type: "commandExecution", Command: fmt.Sprint("echo after-", i)})
	}
	feed := u.view.renderConversation(79)
	u.view.following = false
	for _, tc := range []struct {
		name   string
		offset int
		pinned bool
	}{
		{"above", feed.mainReplyEnd, true},
		{"last row visible", feed.mainReplyEnd - 1, false},
		{"heading visible", feed.mainReplyStart, false},
		{"below", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u.view.offset = tc.offset
			for range 3 {
				u.mainFrame(80, 13, 0)
				if got := u.view.feedTop > 1; got != tc.pinned {
					t.Fatalf("pin=%v, want %v at offset %d", got, tc.pinned, u.view.offset)
				}
			}
		})
	}
	u.view.following = true
	u.mainFrame(80, 24, 0)
	if u.view.feedTop <= 1 {
		t.Fatal("off-screen reply not pinned while following")
	}
	// Incremental scrolling must reach the final rows even though the pin
	// reduces the viewport below the size used for its visibility decision.
	u.view.following = false
	u.view.offset = u.view.feedLines - u.view.feedRows - 2
	for range 3 {
		before := u.view.offset
		u.view.scrollKey('j')
		u.mainFrame(80, 24, 0)
		if !u.view.following && u.view.offset <= before {
			t.Fatal("pin prevented incremental scrolling toward the bottom")
		}
	}
	if !u.view.following {
		t.Fatal("incremental scrolling did not reach bottom")
	}
	u.mainFrame(80, 120, 0)
	if u.view.feedTop != 1 {
		t.Fatal("resize exposed original without removing pin")
	}
}
