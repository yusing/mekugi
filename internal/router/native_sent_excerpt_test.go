package router

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
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
	if strings.Contains(main, "Spawn last line.") || strings.Contains(main, "Follow last line.") || strings.Count(main, "↩ Open assignment") != 2 {
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
	if u.shell.output == nil || !u.shell.activityOpen {
		t.Fatal("link did not open the follow-up in a dialog")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	if full := u.shell.output.laid.Text; !strings.Contains(full, "Follow last line.") {
		t.Fatalf("dialog lost the full follow-up:\n%s", full)
	}
	u.shell.outputKey("\x1b")
	if u.shell.output != nil || !u.shell.activityOpen {
		t.Fatal("dismissing dialog changed Activity navigation")
	}
}

func TestSentExcerptKeepsScrolledViewportStill(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	start := time.Date(2026, 9, 28, 8, 44, 12, 0, time.Local)
	task := strings.Repeat("Remove only the fixtures you created. ", 20)
	v.entries = []liveActivityRecord{
		{Seq: 1, Agent: "Main", Kind: "assignment", Observed: start, activitySeq: 5, assignment: &activityAssignment{to: "/root/fixtures", text: task}, blocks: []activityui.Block{{Kind: "message", From: "/root", To: "/root/fixtures", Body: task}}},
	}
	for i := range 20 {
		entry := activityPaneEntry{Seq: uint64(i + 2), Agent: "Main", Kind: "text", Text: fmt.Sprint("Note ", i, "."), Observed: start}
		v.appendEntry(entry, parseLiveActivity(entry))
	}
	const width, rows = 60, 6
	feed := v.renderFeed(width, rows)
	v.following, v.offset = false, feed.passing[0].end+12
	top := v.viewport(feed, rows)[0]
	if !v.passed[1] {
		t.Fatal("assignment above the viewport was not marked")
	}
	feed = v.renderFeed(width, rows)
	if got := v.viewport(feed, rows)[0]; got != top || !strings.Contains(ansi.Strip(strings.Join(feed.lines, "\n")), "↩ Open assignment") {
		t.Fatalf("collapse moved the viewport: %q, want %q", ansi.Strip(got), ansi.Strip(top))
	}
}

func TestMainBatchCollapsesOutOfView(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	start := time.Date(2026, 9, 28, 8, 44, 12, 0, time.Local)
	for _, entry := range []activityPaneEntry{
		{Seq: 1, Agent: "Main", Kind: "tool", Text: "Search `zzz`\n\nSearch `bbb`\n\nRun `foo`\n\nRead `bar`", Observed: start},
		{Seq: 2, Agent: "Main", Kind: "text", Text: "Checking one more file.", Observed: start},
		{Seq: 3, Agent: "Main", Kind: "tool", Text: "Read `single.go`", Observed: start},
	} {
		v.appendEntry(entry, parseLiveActivity(entry))
	}
	for i := range 20 {
		entry := activityPaneEntry{Seq: uint64(i + 4), Agent: "Main", Kind: "text", Text: fmt.Sprint("Note ", i, "."), Observed: start}
		v.appendEntry(entry, parseLiveActivity(entry))
	}
	const width, rows = 60, 6
	feed := v.renderFeed(width, rows)
	if got := ansi.Strip(strings.Join(feed.lines, "\n")); !strings.Contains(got, "Search zzz · bbb") || !strings.Contains(got, "Read   bar") {
		t.Fatalf("batch in view was collapsed:\n%s", got)
	}
	v.following, v.offset = false, feed.passing[0].end+12
	top := v.viewport(feed, rows)[0]
	if !v.passed[1] {
		t.Fatal("batch above the viewport was not marked")
	}
	feed = v.renderFeed(width, rows)
	got := ansi.Strip(strings.Join(feed.lines, "\n"))
	if !strings.Contains(got, "└ Searched 2 patterns • Read 1 file • Ran 1 command") || strings.Contains(got, "zzz") {
		t.Fatalf("batch out of view did not collapse to one row:\n%s", got)
	}
	if row := v.viewport(feed, rows)[0]; row != top {
		t.Fatalf("collapse moved the viewport: %q, want %q", ansi.Strip(row), ansi.Strip(top))
	}
	if !strings.Contains(got, "single.go") {
		t.Fatalf("a lone operation collapsed:\n%s", got)
	}
	row := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(line, "Searched") })
	u := &terminalUI{}
	if !u.openOutput(v, feed.snippets[row]) || len(u.output.pages) != 3 {
		t.Fatalf("collapsed batch did not open its operations in the dialog: %+v", u.output)
	}
}

func TestMainBatchKeepsUnconfirmedHostItemsOutOfView(t *testing.T) {
	for _, status := range []string{"completed", "declined", "failed"} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.view.conversation = true
		search := map[string]any{"id": "search", "type": "commandExecution", "command": "rg zzz", "status": status,
			"commandActions": []map[string]any{{"type": "search", "command": "rg zzz", "query": "zzz"}}}
		if status == "completed" {
			search["exitCode"] = 0
		}
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": search})
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
			"id": "read", "type": "commandExecution", "command": "cat bar", "status": "completed", "exitCode": 0,
			"commandActions": []map[string]any{{"type": "read", "path": "bar", "command": "cat bar"}}}})
		for i := range 20 {
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
				"id": fmt.Sprint("note-", i), "type": "agentMessage", "text": fmt.Sprint("Note ", i, ".")}})
		}
		const width, rows = 60, 6
		v := u.view
		feed := v.renderFeed(width, rows)
		v.following, v.offset = false, len(feed.lines)-rows
		v.viewport(feed, rows)
		got := ansi.Strip(strings.Join(v.renderFeed(width, rows).lines, "\n"))
		if collapsed := strings.Contains(got, "Searched 1 pattern • Read 1 file"); collapsed != (status == "completed") || !collapsed && !strings.Contains(got, "zzz") {
			t.Fatalf("%s search out of view collapsed=%v:\n%s", status, collapsed, got)
		}
	}
}

func TestUISnapshotMainBatchWithFailedCommand(t *testing.T) {
	for _, mode := range []string{"live", "resume"} {
		t.Run(mode, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			var items []appServerItem
			for i, command := range []string{"rg zzz", "rg bbb", "cat bar", "go env GOMODCACHE"} {
				item := appServerItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: command, Status: "completed", ExitCode: new(0)}
				if i == 1 {
					item.Status, item.ExitCode = "failed", new(2)
				}
				items = append(items, item)
			}
			items = append(items, appServerItem{ID: "edit", Type: "fileChange", Status: "completed", Changes: []appServerFileChange{{Path: "bar.go", Diff: "+new\n-old\n"}, {Path: "baz.go", Diff: "+other\n"}}})
			if mode == "resume" {
				u.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: items}})
			} else {
				for _, item := range items {
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
				}
				for i := range 20 {
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": appServerItem{ID: fmt.Sprint("note-", i), Type: "agentMessage", Text: fmt.Sprint("Note ", i, ".")}})
				}
			}
			finishPacing(u.view, u.agents)
			v := u.view
			feed := v.renderFeed(100, 6)
			v.viewport(feed, 6)
			feed = v.renderFeed(100, 6)
			assertNativeUISnapshot(t, "native-main-batch-failed-command", feed.lines[:1])
			v.following, v.offset = false, 0
			screen := vt.NewEmulator(120, 28)
			defer screen.Close()
			if err := u.paint(screen, 120, 28); err != nil {
				t.Fatal(err)
			}
			for y, row := range strings.Split(screen.String(), "\n") {
				if x := strings.Index(row, "Searched 2 patterns"); x >= 0 {
					x = ansi.StringWidth(row[:x])
					for _, key := range []byte(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1)) {
						if err := u.shell.key(key); err != nil {
							t.Fatal(err)
						}
					}
					break
				}
			}
			if u.shell.output == nil || len(u.shell.output.pages) != 6 ||
				!slices.ContainsFunc(u.shell.output.pages, func(b activityui.Block) bool { return b.ExitCode == 2 }) ||
				!slices.ContainsFunc(u.shell.output.pages, func(b activityui.Block) bool { return b.Lang == "diff" && strings.Contains(b.Code, "+new") }) ||
				!slices.ContainsFunc(u.shell.output.pages, func(b activityui.Block) bool { return b.Lang == "diff" && strings.Contains(b.Code, "+other") }) {
				t.Fatal("rendered batch click did not open failed-command and edit details")
			}
		})
	}
}
