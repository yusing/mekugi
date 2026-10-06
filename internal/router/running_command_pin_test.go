package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotNativeRunningCommands(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.clock = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local) }
	for _, v := range []*liveActivityView{u.view, u.agents} {
		v.clock, v.painter.Theme = u.clock, livediff.DarkTheme
		v.feedOnly, v.bare = true, true
	}
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	command := func(thread, id, cmd, status string) {
		method, item := "item/started", map[string]any{"id": id, "type": "commandExecution", "command": cmd, "status": status}
		if status != "inProgress" {
			method = "item/completed"
			item["exitCode"] = 0
			if status == "failed" {
				item["exitCode"] = 1
			}
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": item})
	}
	message := func(thread, id, text string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{
			"id": id, "type": "agentMessage", "text": text}})
	}
	var snapshot strings.Builder
	frame := func(label string) {
		t.Helper()
		for i, v := range []*liveActivityView{u.view, u.agents} {
			fmt.Fprintf(&snapshot, "== %s %s ==\n", label, []string{"Main", "Activity"}[i])
			snapshot.WriteString(strings.Join(v.render(70, 10, u.now()), "\n") + "\n")
		}
	}
	for _, thread := range []string{"main", "child"} {
		message(thread, "before", "FIRST_TRANSCRIPT_ROW\n"+strings.Repeat("Earlier transcript\n", 11))
		command(thread, "test", "go test ./...", "inProgress")
		command(thread, "build", "make build", "inProgress")
	}
	frame("visible")
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{
		"threadId": "child", "turnId": "t", "itemId": "test", "delta": "progress one\nprogress two\nprogress three\n"})
	rollCommandOutput(u)
	for _, thread := range []string{"main", "child"} {
		message(thread, "after", strings.Repeat("Later transcript\n", 12)+"Last row")
	}
	frame("following")
	for _, v := range []*liveActivityView{u.view, u.agents} {
		clicked := false
		for row, snippet := range v.feedSnippets {
			if block, ok := v.snippetBlock(snippet); ok && block.Code == "go test ./..." {
				clicked = v.pointSnippet('\r', v.feedTop+row, v.feedLeft+1)
				break
			}
		}
		block, ok := v.snippetBlock(v.opening)
		if !clicked || !ok || !block.Running || block.Code != "go test ./..." {
			t.Fatalf("pinned click opened a different command: %+v", block)
		}
		v.following, v.offset = false, 0
	}
	feed := u.agents.renderFeed(70, 5)
	u.agents.offset = feed.running[0]
	feed = u.agents.renderFeed(70, 5)
	if shown := ansi.Strip(strings.Join(u.agents.viewport(feed, 5), "\n")); !strings.Contains(shown, "Running go test ./...") {
		t.Fatalf("agent heading covered the command at the viewport start: %s", shown)
	}
	u.agents.offset = 0
	frame("paused before commands")
	for _, v := range []*liveActivityView{u.view, u.agents} {
		v.pendingTarget = v.entries[0].Seq
		if shown := ansi.Strip(strings.Join(v.render(70, 10, u.now()), "\n")); !strings.Contains(shown, "FIRST_TRANSCRIPT_ROW") {
			t.Fatalf("pins hid the start or navigation target: %s", shown)
		}
	}
	for _, thread := range []string{"main", "child"} {
		command(thread, "test", "go test ./...", "completed")
	}
	frame("one complete")
	for _, v := range []*liveActivityView{u.view, u.agents} {
		if v.following || v.offset != 0 {
			t.Fatal("completion moved the paused transcript")
		}
		for _, height := range []int{1, 3} {
			feed := v.renderFeed(25, height)
			rows := v.viewport(feed, height)
			if len(rows) != height || !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "Running") {
				t.Fatalf("small pane lost its running command: %q", rows)
			}
			v.following = true
			feed = v.renderFeed(25, height)
			rows = v.viewport(feed, height)
			if height > 1 && rows[height-1] != feed.lines[len(feed.lines)-1] {
				t.Fatal("pins left the following viewport above the last transcript row")
			}
			v.following, v.offset = false, 0
		}
	}
	u.agents.only, u.agents.selected = true, "/root/someone-else"
	if feed := u.agents.renderFeed(70, 10); len(feed.running) != 0 {
		t.Fatal("filter retained another agent's pinned command")
	}
	u.agents.only = false
	for _, thread := range []string{"main", "child"} {
		command(thread, "build", "make build", "failed")
	}
	frame("finished")
	uisnapshot.Assert(t, "testdata/snapshots/native-running-commands.txt", snapshot.String())
}

func TestRunningPinTracksActiveSegment(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	script := "pwd && sleep 1"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	report.send(
		execsegment.Message{Type: execsegment.Begin, Index: 0},
		execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)},
		execsegment.Message{Type: execsegment.Begin, Index: 1},
	)
	awaitMain(t, u, "Running sleep 1")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "later", "type": "agentMessage", "text": strings.Repeat("Later event\n", 12)}})
	feed := u.view.renderFeed(70, 5)
	rows := ansi.Strip(strings.Join(u.view.viewport(feed, 5), "\n"))
	if len(feed.running) != 1 || !strings.Contains(rows, "Running sleep 1") || strings.Contains(rows, "pwd") {
		t.Fatalf("pins did not follow the active segment: %s", rows)
	}
}

func TestRunningPinQuestionHover(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	v := u.view
	v.feedTop, v.feedLeft, v.feedRight = 1, 1, 70
	feed := liveActivityFeed{
		lines: []string{"Running test", "earlier", "earlier", "↩ first question", "↩ second question", "body", "tail"},
		heads: []int{0, 1, 2, 3, 4, 5, 6}, snippets: make([]liveActivitySnippet, 7), questions: []uint64{0, 0, 0, 42, 43, 0, 0},
	}
	for _, running := range [][]int{nil, {0}} {
		feed.running = running
		v.following, v.offset = false, 3
		v.viewport(feed, 4)
		for pointed, target := range v.feedQuestions {
			if target == 0 {
				continue
			}
			v.pointQuestion(v.feedTop+pointed, v.feedLeft)
			for row, line := range v.viewport(feed, 4) {
				if strings.Contains(line, "\x1b[4m") != (row == pointed) {
					t.Fatalf("pins=%v hover=%d row=%d: %q", running, pointed, row, line)
				}
			}
		}
	}
}

func TestRunningCommandSurvivesFeedRetention(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	item := map[string]any{"id": "long", "type": "commandExecution", "command": "make test", "status": "inProgress"}
	notify := func(method string) {
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
	}
	notify("item/started")
	entries := make([]activityPaneEntry, liveActivityFeedLimit)
	for i := range entries {
		entries[i] = activityPaneEntry{Seq: u.view.lastSeq + uint64(i) + 1, Agent: "Main", Kind: "text", Text: "Later event"}
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: entries})
	feed := u.view.renderFeed(70, 10)
	if len(feed.running) != 1 || !strings.Contains(ansi.Strip(strings.Join(u.view.viewport(feed, 10), "\n")), "Running make test") {
		t.Fatal("retention released a running command")
	}
	// Return to the host sequence after inserting the retained event fixture.
	u.session.seq = u.view.lastSeq
	item["status"], item["exitCode"] = "completed", 0
	notify("item/completed")
	if feed := u.view.renderFeed(70, 10); len(feed.running) != 0 {
		t.Fatal("retained command did not unpin on host completion")
	}
}
