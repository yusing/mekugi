package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestNativeSentMessagesBecomeExcerptsOutOfView(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.turn = "main-turn"
	u.shell.side = false
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	collab := func(id, tool, prompt string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": u.turn, "item": map[string]any{"id": id, "type": "collabAgentToolCall",
			"tool": tool, "status": "completed", "senderThreadId": "main", "receiverThreadIds": []string{"child"}, "prompt": prompt}})
	}
	long := func(label string) string {
		return label + " first line.\n\n" + strings.Repeat(label+" detail.\n", 8) + label + " last line."
	}
	collab("spawn", "spawnAgent", long("Spawn"))
	collab("follow", "followupTask", long("Follow"))
	paint := func() string {
		t.Helper()
		if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
			t.Fatal(err)
		}
		return ansi.Strip(strings.Join(u.view.renderFeed(110, 50).lines, "\n"))
	}
	// The thread has moved past the spawn, which shrinks in place as before.
	if main := paint(); !strings.Contains(main, "Spawn first line.") || !strings.Contains(main, "Follow last line.") || strings.Contains(main, "Open assignment") {
		t.Fatalf("messages in view were shortened:\n%s", main)
	}
	for i := range 30 {
		u.view.applyAppServerItem("", "main", "main", u.turn, fmt.Sprint("note-", i), "item/completed", "", appServerItem{Type: "agentMessage", Text: fmt.Sprint("Main note ", i, ".")})
	}
	paint() // Scrolls both messages above the viewport.
	main := paint()
	if strings.Contains(main, "Spawn last line.") || strings.Contains(main, "Follow last line.") || strings.Count(main, "↩ Open assignment in Activity") != 2 {
		t.Fatalf("messages out of view did not become linked excerpts:\n%s", main)
	}
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	var seq, activity uint64
	for _, entry := range u.view.entries {
		if entry.Kind == "assignment" {
			seq, activity = entry.Seq, entry.activitySeq
		}
	}
	if seq == 0 || activity == 0 || !u.shell.openActivityReply(seq) {
		t.Fatalf("follow-up has no Activity target: seq=%d activity=%d", seq, activity)
	}
	if !u.shell.activityOpen || u.agents.selected != "/root/worker" || u.agents.pendingTarget != activity {
		t.Fatalf("link did not open the follow-up in Activity: open=%v selected=%q pending=%d", u.shell.activityOpen, u.agents.selected, u.agents.pendingTarget)
	}
	if full := ansi.Strip(strings.Join(u.agents.renderFeed(100, 100).lines, "\n")); !strings.Contains(full, "Follow last line.") {
		t.Fatalf("Activity lost the full follow-up:\n%s", full)
	}
}

func TestSentExcerptKeepsScrolledViewportStill(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	start := time.Date(2026, 9, 28, 8, 44, 12, 0, time.Local)
	task := strings.Repeat("Remove only the fixtures you created. ", 20)
	v.entries = []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "assignment", Observed: start, activitySeq: 5, assignment: &activityAssignment{to: "/root/fixtures", text: task}}}
	v.blocks = [][]activityui.Block{{{Kind: "message", From: "/root", To: "/root/fixtures", Body: task}}}
	for i := range 20 {
		entry := activityPaneEntry{Seq: uint64(i + 2), Agent: "Main", Kind: "text", Text: fmt.Sprint("Note ", i, "."), Observed: start}
		v.entries, v.blocks = append(v.entries, entry), append(v.blocks, parseLiveActivity(entry))
	}
	const width, rows = 60, 6
	feed := v.renderFeed(width, rows)
	v.following, v.offset = false, feed.sent[0].end+4
	top := v.viewport(feed, rows)[0]
	if !v.passed[1] {
		t.Fatal("assignment above the viewport was not marked")
	}
	feed = v.renderFeed(width, rows)
	if got := v.viewport(feed, rows)[0]; got != top || !strings.Contains(ansi.Strip(strings.Join(feed.lines, "\n")), "↩ Open assignment in Activity") {
		t.Fatalf("collapse moved the viewport: %q, want %q", ansi.Strip(got), ansi.Strip(top))
	}
}
