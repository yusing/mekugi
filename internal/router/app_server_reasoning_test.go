package router

import (
	"bytes"
	json "encoding/json/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

// Existing lifecycle checks observe fully played frames. Pacing itself is
// covered separately with explicit individual flushes.
func reasoningTestNotify(t *testing.T, u *appServerUI, method string, params map[string]any) {
	t.Helper()
	appServerTestNotify(t, u, method, params)
	rollCommandOutput(u)
}

func TestAppServerPublicSummaryNotifications(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	for _, thread := range []string{"main", "child"} {
		reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		for _, delta := range []string{"**Checking " + thread + "**", "\n\nPublic " + thread + " summary."} {
			reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "same-id", "delta": delta})
		}
		reasoningTestNotify(t, u, "item/reasoning/textDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "same-id", "delta": "PRIVATE"})
	}
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	child := ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, "Public main summary.") || strings.Contains(main, "Public child") || !strings.Contains(child, "Public child summary.") || strings.Contains(child, "Public main") {
		t.Fatalf("summary routing: main=%q, child=%q", main, child)
	}
	for _, thread := range []string{"main", "child"} {
		// Real reasoning content is a string array, unlike user input blocks.
		reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{
			"id": "same-id", "type": "reasoning", "summary": []string{"**Complete**\n\nFinal public " + thread + "."}, "content": []string{"PRIVATE"}}})
		reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
	}
	u.agents.only, u.agents.selected = false, "/root/worker"
	main = ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	child = ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if strings.Count(main, "• Complete") != 1 || strings.Count(child, "• Complete") != 1 || !strings.Contains(main, "Final public main.") || !strings.Contains(child, "Final public child.") || strings.Contains(main+child, "PRIVATE") || strings.Contains(main+child, "Public main summary") {
		t.Fatalf("completed summaries: main=%q, child=%q", main, child)
	}
	// A reused item ID in a new turn must not replace earlier summaries.
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "next", "itemId": "same-id", "delta": "Next public summary."})
	main = ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, "• Complete") || !strings.Contains(main, "Next public summary.") || u.view.entries[0].Text != "**Complete**\n\nFinal public main." {
		t.Fatalf("turn identity lost: %q", main)
	}
}

func TestAppServerRestorePublicSummaries(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	var turns []appServerHistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"t","status":"completed","items":[{"id":"r","type":"reasoning","summary":["Public restored summary."],"content":["PRIVATE"]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	u.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: turns})
	u.agents.only, u.agents.selected = false, "/root/worker"
	for _, view := range []*liveActivityView{u.view, u.agents} {
		feed := view.renderFeed(90, 40)
		got := ansi.Strip(strings.Join(feed.lines, "\n"))
		if !strings.Contains(got, "• Public restored summary.") || strings.Count(got, "Public restored summary.") != 1 || strings.Contains(got, "PRIVATE") {
			t.Fatalf("restored summary did not fold: %q", got)
		}
		row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool { return s != liveActivitySnippet{} })
		if row >= 0 {
			t.Fatal("fully shown restored reasoning has a redundant detail target")
		}
	}
	// Raw-only items are decodable but produce no visible entry.
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"raw","type":"reasoning","summary":[],"content":["PRIVATE"]}}}`)
	if len(u.view.entries) != 1 {
		t.Fatalf("raw reasoning reached transcript: %+v", u.view.entries)
	}
}

func TestAppServerComposerKeepsWorkingDuringReasoning(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "**Checking layout**"})
	for _, dock := range []int{0, 3} {
		frame, dockRect := u.mainFrame(70, 12, dock)
		border := u.composerRect.y - 1
		if len(frame) != 12 || !strings.Contains(ansi.Strip(frame[border]), "◐ Working") || strings.Contains(ansi.Strip(frame[border]), "Checking layout") || dockRect.y+dockRect.h != border-1 || frame[border-1] != "" {
			t.Fatalf("reasoning replaced Working: dock=%d frame=%q", dock, frame)
		}
	}
	for _, size := range [][2]int{{1, 1}, {7, 3}, {12, 4}} {
		frame, _ := u.mainFrame(size[0], size[1], 0)
		if len(frame) != size[1] {
			t.Fatalf("height overflow: %q", frame)
		}
		for _, row := range frame[u.composerRect.y:] {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("width overflow: %q", row)
			}
		}
	}
}

func TestAppServerWorkingShimmers(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	a, b := u.sessionLabel(u.turnStarted), u.sessionLabel(u.turnStarted.Add(500*time.Millisecond))
	if a == b || ansi.Strip(a) != ansi.Strip(b) || !strings.Contains(ansi.Strip(a), "Working") {
		t.Fatalf("no text-preserving shimmer: %q %q", a, b)
	}
	u.turn = ""
	if u.sessionLabel(u.turnStarted) != u.sessionLabel(u.turnStarted.Add(500*time.Millisecond)) {
		t.Fatal("idle label animates")
	}
}

func TestAppServerCompletedElapsedTime(t *testing.T) {
	for seconds, elapsed := range map[int]string{0: "0s", 12: "12s", 60: "1m", 75: "1m15s", 82: "1m22s", 3600: "1h", 3682: "1h1m22s"} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
		u.turnStarted = time.Now().Add(-time.Duration(seconds) * time.Second)
		reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "completed"}})
		want := "Completed in " + elapsed
		for _, now := range []time.Time{time.Now(), time.Now().Add(time.Minute)} {
			if got := ansi.Strip(u.sessionLabel(now)); got != want {
				t.Fatalf("completion label = %q, want %q", got, want)
			}
		}
	}
}

func TestAppServerWorkingSurvivesDockComposition(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.draft = "one\ntwo\nthree\nfour"
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	u.shell.preview(diffview.Preview{ID: "p", Caller: "/root", Status: diffview.PreviewEdit, Input: "package a\n"})
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "**Active summary**"})
	var out bytes.Buffer
	if err := u.paint(&out, 80, 14); err != nil {
		t.Fatal(err)
	}
	screen := vt.NewEmulator(80, 14)
	defer screen.Close()
	if _, err := screen.Write(out.Bytes()); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(screen.String(), "\n")
	for i, row := range rows {
		if strings.Contains(row, "╭─") && strings.Contains(row, "Working") {
			if i == 0 || strings.Contains(row, "Active summary") {
				t.Fatalf("dock hid Working:\n%s", screen.String())
			}
			return
		}
	}
	t.Fatalf("composer missing:\n%s", screen.String())
}

func TestAppServerPendingStatusPulses(t *testing.T) {
	for _, status := range []string{"Connecting…", "Starting thread…", "Resuming thread…", "Sending…", "Interrupting…", "Restoring roster and Activity…"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.status = status
			u.thread = ""
			if status == "Sending…" {
				u.thread, u.submission.text = "main", "hello"
			}
			if status == "Interrupting…" {
				u.thread, u.turn = "main", "turn"
			}
			if strings.HasPrefix(status, "Restoring") {
				u.thread = "main"
				if err := u.restorePaneContent(appServerThreadInfo{ID: "main"}); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Unix(0, 0)
			dim, bright, dimAgain := u.sessionLabel(start), u.sessionLabel(start.Add(time.Second)), u.sessionLabel(start.Add(2*time.Second))
			if !u.sessionAnimating() || dim == bright || dim != dimAgain || ansi.Strip(dim) != ansi.Strip(bright) {
				t.Fatalf("pending status does not pulse: %q %q %q", dim, bright, dimAgain)
			}
			u.alert = true
			if u.sessionAnimating() || u.sessionLabel(start) != u.sessionLabel(start.Add(time.Second)) {
				t.Fatal("alert animates")
			}
			u.restoring = nil
			u.alert, u.thread, u.turn, u.submission.text, u.status = false, "main", "", "", "Ready"
			if u.sessionAnimating() || u.sessionLabel(start) != u.sessionLabel(start.Add(time.Second)) {
				t.Fatal("idle animates")
			}
		})
	}
}

// Third-party reasoning arrives untitled; it streams as a thinking block and
// completes with its time, including when the turn ends without completing it.
func TestAppServerProviderThinking(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t"}})
	u.agents.only, u.agents.selected = false, "/root/worker"
	feed := func() string { return ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n")) }
	for _, delta := range []string{"First ", "thought.\n\nSecond.\n\nThird.\n\nFourth."} {
		reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "r1", "delta": delta})
	}
	if got := feed(); !strings.Contains(got, "• Thinking… · +1 line") || strings.Contains(got, "First thought.") || !strings.Contains(got, "Fourth.") {
		t.Fatalf("live thinking: %q", got)
	}
	// A route without a replayable summary completes with an empty one.
	reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "r1", "type": "reasoning", "summary": []string{}}})
	// A sub-second block names no duration rather than "0s".
	if got := feed(); !strings.Contains(got, "• Thought") || strings.Contains(got, "Thought for") || !strings.Contains(got, "First thought.") || strings.Contains(got, "Thinking…") {
		t.Fatalf("completed thinking: %q", got)
	}
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "r2", "delta": "Interrupted thought."})
	reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t", "status": "interrupted"}})
	if got := feed(); strings.Contains(got, "Thinking…") || strings.Count(got, "• Thought") != 1 || strings.Count(got, "• Interrupted thought.") != 1 {
		t.Fatalf("turn end left thinking live: %q", got)
	}
}

// Finished long reasoning folds after later activity and a quiet period;
// a click opens it in the shared dialog.
func TestAppServerThinkingFolds(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	u.agents.only, u.agents.selected = false, "/root/worker"
	body := "**Reviewing**\n\nAlpha.\n\nBravo.\n\nCharlie.\n\nDelta."
	for _, thread := range []string{"main", "child"} {
		reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": body})
		reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{body}}})
		nextEvent(t, u, thread)
	}
	settleActivity(time.Now().Add(time.Minute), u.view, u.agents)
	for name, view := range map[string]*liveActivityView{"Main": u.view, "Activity": u.agents} {
		t.Run(name, func(t *testing.T) {
			render := func() liveActivityFeed { return view.renderFeed(90, 40) }
			plain := func(feed liveActivityFeed) string { return ansi.Strip(strings.Join(feed.lines, "\n")) }
			feed := render()
			if got := plain(feed); !strings.Contains(got, "• Reviewing") || strings.Contains(got, "Alpha.") {
				t.Fatalf("finished thinking did not fold: %q", got)
			}
			row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool { return s != liveActivitySnippet{} })
			if row < 0 || !strings.Contains(ansi.Strip(feed.lines[row]), "Reviewing") {
				t.Fatal("folded thinking cannot expand")
			}
			if !u.shell.openOutput(view, feed.snippets[row]) {
				t.Fatal("folded thinking did not request dialog")
			}
			page := view.painter.DialogPage(u.shell.output.pages[0], 80)
			if page.Text != body || len(page.Lines) < 4 {
				t.Fatalf("dialog lost full reasoning: %+v", page)
			}
			if got := plain(render()); strings.Contains(got, "Alpha.") {
				t.Fatalf("dialog request unfolded thinking in the feed: %q", got)
			}
		})
	}
}

func TestThinkingElapsedNamesWholeSecondsOnly(t *testing.T) {
	for thought, want := range map[time.Duration]string{0: "", 900 * time.Millisecond: "", time.Second: "1s", 12500 * time.Millisecond: "12s"} {
		entry := activityPaneEntry{Kind: "reasoning", Text: "Body.", native: &liveActivityNativeItem{phase: "item/completed", thought: thought}}
		if got := parseLiveActivity(entry)[0].Elapsed; got != want {
			t.Errorf("thought %v: elapsed %q, want %q", thought, got, want)
		}
	}
}

// As in grok-build, a provider request that streams untitled reasoning shows
// its thinking block from the request start, before the first delta. The
// first reasoning item takes the block over and times from that start; other
// output first, or the turn's end, removes the empty block.
func TestAppServerThinkingFromRequestStart(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	activity := newSubagentActivity()
	activity.attachNativePane("main")
	activity.observe("child", "main", "/root/worker", true)
	u.proxy = &mekugiProxy{activity: activity}
	reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	u.agents.only, u.agents.selected = false, "/root/worker"
	feed := func(view *liveActivityView) string {
		return ansi.Strip(strings.Join(view.renderFeed(90, 40).lines, "\n"))
	}
	start := func(thread string, ago time.Duration) {
		t.Helper()
		activity.beginResponse(thread, true)
		activity.starts[len(activity.starts)-1].at = time.Now().Add(-ago)
		u.applyObservedActivity()
	}
	thinkingRows := func(view *liveActivityView) int {
		rows := 0
		for _, entry := range view.entries {
			if entry.Kind == "reasoning" {
				rows++
			}
		}
		return rows
	}

	// Main and child each show the block while waiting for the first delta.
	start("main", 3*time.Second)
	start("child", 3*time.Second)
	for name, view := range map[string]*liveActivityView{"Main": u.view, "Activity": u.agents} {
		if got := feed(view); !strings.Contains(got, "• Thinking…") {
			t.Fatalf("%s shows no thinking before the first delta: %q", name, got)
		}
	}
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t"}})
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "r1", "delta": "Planning."})
	if got := feed(u.agents); thinkingRows(u.agents) != 1 || strings.Contains(got, "Thinking…") || !strings.Contains(got, "• Planning.") {
		t.Fatalf("first delta did not take over the block: %d rows, %q", thinkingRows(u.agents), got)
	}
	reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "r1", "type": "reasoning", "summary": []string{"Planning."}}})
	if got := feed(u.agents); thinkingRows(u.agents) != 1 || !strings.Contains(got, "• Planning for 3s") {
		t.Fatalf("thinking not timed from the request start: %q", got)
	}

	// A request that answers without reasoning leaves no empty block.
	start("child", 0)
	reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "ls"}})
	if got := feed(u.agents); thinkingRows(u.agents) != 1 || strings.Contains(got, "Thinking…") {
		t.Fatalf("empty thinking block kept after other output: %q", got)
	}
	// The previous request's late command completion keeps the next block.
	start("child", 0)
	reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "ls", "status": "completed"}})
	if got := feed(u.agents); !strings.Contains(got, "• Thinking…") {
		t.Fatalf("late command completion removed the next request's block: %q", got)
	}
	reasoningTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "a1", "delta": "Done."})
	if got := feed(u.agents); strings.Contains(got, "Thinking…") {
		t.Fatalf("empty thinking block kept after an answer delta: %q", got)
	}
	// Main's answer, handled by the transcript, also removes its block.
	reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "m", "item": map[string]any{"id": "a2", "type": "agentMessage", "text": ""}})
	if got := feed(u.view); thinkingRows(u.view) != 0 || strings.Contains(got, "Thinking…") {
		t.Fatalf("empty thinking block kept after Main's answer: %q", got)
	}
	// Main's pending block ends with its turn.
	start("main", 0)
	reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "m", "status": "completed"}})
	if got := feed(u.view); thinkingRows(u.view) != 0 || strings.Contains(got, "Thinking…") || strings.Contains(got, "Thought") {
		t.Fatalf("turn end kept an empty thinking block: %q", got)
	}

	// Only providers streaming untitled reasoning start a block.
	activity.beginResponse("main", false)
	if starts := activity.takeRequestStarts("main"); len(starts) != 0 {
		t.Fatalf("Codex-summarized request started a thinking block: %+v", starts)
	}
	activity.observe("other", "", "/root", false)
	activity.beginResponse("other", true)
	if starts := activity.takeRequestStarts("main"); len(starts) != 0 || len(activity.starts) != 0 {
		t.Fatalf("request start crossed roots: %+v, retained %+v", starts, activity.starts)
	}
}

func TestAppServerStartedSummarySettlesWithoutDelta(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "interrupted"}[interrupted], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.agents.only, u.agents.selected = false, "/root/worker"
			for _, thread := range []string{"main", "child"} {
				reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
				reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{"**Checking**\n\nStarted public body."}}})
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				if got := ansi.Strip(strings.Join(view.renderFeed(90, 40).lines, "\n")); strings.Contains(got, "Thinking…") || !strings.Contains(got, "• Checking") || !strings.Contains(got, "Started public body.") {
					t.Fatalf("started summary is not streaming: %s", got)
				}
				if interrupted {
					reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "interrupted"}})
				} else {
					reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{}}})
				}
				nextEvent(t, u, thread)
				settleActivity(time.Now().Add(time.Minute), view)
				feed := view.renderFeed(90, 40)
				if got := ansi.Strip(strings.Join(feed.lines, "\n")); strings.Contains(got, "Thinking…") || !strings.Contains(got, "• Checking") || strings.Contains(got, "Started public body.") {
					t.Fatalf("started summary did not settle: %s", got)
				}
				row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool { return s != liveActivitySnippet{} })
				if row < 0 {
					t.Fatal("folded started summary has no detail target")
				}
			}
		})
	}
}

func TestUISnapshotAppServerReasoningStartsBeforeSummary(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			at := time.Date(2026, 9, 30, 8, 38, 54, 904000000, time.Local)
			u.clock = func() time.Time { return at }
			u.view.clock, u.agents.clock = u.clock, u.clock
			u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
			reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.agents.only, u.agents.selected = false, "/root/worker"
			view := u.view
			if thread == "child" {
				view = u.agents
			}
			reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
			reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{}}})
			frame := func() string { return ansi.Strip(strings.Join(view.renderFeed(80, 40).lines, "\n")) + "\n" }
			t.Run("waiting", func(t *testing.T) {
				uisnapshot.Assert(t, "testdata/snapshots/reasoning-waiting-"+thread+".txt", frame())
			})
			// Public summary can arrive only just before completion. Its
			// arrival is not the beginning of the reasoning item.
			at = at.Add(4200 * time.Millisecond)
			reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": "**Checking**\n\nPublic summary."})
			at = at.Add(83 * time.Millisecond)
			reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{"**Checking**\n\nPublic summary."}}})
			if got := frame(); strings.Count(got, "Checking for 4s") != 1 || len(u.session.thinking) != 0 {
				t.Fatalf("summary burst lost observed item duration or duplicated header: %s", got)
			}
			uisnapshot.Assert(t, "testdata/snapshots/reasoning-late-summary-"+thread+".txt", frame())
		})
	}
}

func TestUISnapshotAppServerReasoningLifecycle(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, tc := range []struct{ name, body string }{
			{"heading", "**Checking tests**\n\n"},
			{"hash-heading", "# Checking tests\n<!-- -->"},
			{"long", "**Checking tests**\n\nFirst checkpoint.\n\nSecond checkpoint.\n\nThird checkpoint.\n\nFourth checkpoint."},
			{"sections", "**Finalizing execution journal**\n\nFirst retained paragraph.\n\n**Preparing final renderings**\n\nLast retained paragraph."},
		} {
			t.Run(thread+"_"+tc.name, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
				u.clock = func() time.Time { return at }
				u.view.clock, u.agents.clock = u.clock, u.clock
				u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
				reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.agents.only, u.agents.selected = false, "/root/worker"
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
				reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning"}})
				reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": tc.body})
				snapshot := func(stage string) {
					t.Helper()
					t.Run(stage, func(t *testing.T) {
						uisnapshot.Assert(t, "testdata/snapshots/reasoning-lifecycle-"+thread+"-"+tc.name+"-"+stage+".txt", strings.Join(view.renderFeed(80, 40).lines, "\n")+"\n")
					})
				}
				snapshot("streaming")
				at = at.Add(2 * time.Second)
				// Completion may omit text already delivered in deltas.
				reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning"}})
				snapshot("done")
				reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
				nextEvent(t, u, thread)
				if len(u.session.thinking) != 0 || len(u.session.summaries) != 0 || len(view.entries) != 2 {
					t.Fatalf("reasoning completion retained live state or duplicated entries: %+v", view.entries)
				}
			})
		}
	}
}

func TestAppServerEmptyStartedReasoningDoesNotLeaveThought(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
		reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{}}})
		reasoningTestNotify(t, u, "item/reasoning/textDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "PRIVATE"})
		if interrupted {
			reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "interrupted"}})
		} else {
			reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{}}})
		}
		got := ansi.Strip(strings.Join(u.view.renderFeed(80, 40).lines, "\n"))
		if strings.Contains(got, "Thinking") || strings.Contains(got, "Thought") || strings.Contains(got, "PRIVATE") || len(u.session.thinking) != 0 {
			t.Fatalf("empty reasoning left visible or unfinished state: %s", got)
		}
	}
}
