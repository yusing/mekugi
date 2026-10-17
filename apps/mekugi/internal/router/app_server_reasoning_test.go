package router

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
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
	main := ansi.Strip(strings.Join(u.view.RenderFeed(90, 40).Lines, "\n"))
	child := ansi.Strip(strings.Join(u.agents.RenderFeed(90, 40).Lines, "\n"))
	if !strings.Contains(main, "Public main summary.") || strings.Contains(main, "Public child") || !strings.Contains(child, "Public child summary.") || strings.Contains(child, "Public main") {
		t.Fatalf("summary routing: main=%q, child=%q", main, child)
	}
	for _, thread := range []string{"main", "child"} {
		// Real reasoning content is a string array, unlike user input blocks.
		reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{
			"id": "same-id", "type": "reasoning", "summary": []string{"**Complete**\n\nFinal public " + thread + "."}, "content": []string{"PRIVATE"}}})
		reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
	}
	u.agents.setDisplayFilter(false, "/root/worker")
	main = ansi.Strip(strings.Join(u.view.RenderFeed(90, 40).Lines, "\n"))
	child = ansi.Strip(strings.Join(u.agents.RenderFeed(90, 40).Lines, "\n"))
	if strings.Count(main, "• Complete") != 1 || strings.Count(child, "• Complete") != 1 || !strings.Contains(main, "Final public main.") || !strings.Contains(child, "Final public child.") || strings.Contains(main+child, "PRIVATE") || strings.Contains(main+child, "Public main summary") {
		t.Fatalf("completed summaries: main=%q, child=%q", main, child)
	}
	// A reused item ID in a new turn must not replace earlier summaries.
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "next", "itemId": "same-id", "delta": "Next public summary."})
	main = ansi.Strip(strings.Join(u.view.RenderFeed(90, 40).Lines, "\n"))
	if !strings.Contains(main, "• Complete") || !strings.Contains(main, "Next public summary.") || u.view.Entries[0].Source.Text != "**Complete**\n\nFinal public main." {
		t.Fatalf("turn identity lost: %q", main)
	}
}

func TestAppServerOldTurnCompletionPreservesCurrentTranscript(t *testing.T) {
	u, at := newActiveWorkTestUI(t)
	activity := sessioncontrol.NewAudience(nil)
	activity.Attach("main")
	activity.Observe("child", "main", "/root/worker", true)
	setTestAppServerIntegration(u, &observationFixture{activity: activity})
	u.agents.setDisplayFilter(false, "/root/worker")
	wait := binding.ThreadItem{ID: "old-wait", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress"}
	reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "old", "item": wait})
	activity.BeginResponse("child", true, at.Add(-3*time.Second))
	u.applyObservedActivity()
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "current"}})
	oldEnd := map[string]any{"threadId": "child", "turn": map[string]any{"id": "old", "status": "completed"}}
	reasoningTestNotify(t, u, "turn/completed", oldEnd)
	if got := ansi.Strip(strings.Join(u.agents.RenderFeed(90, 40).Lines, "\n")); !strings.Contains(got, "Thinking") {
		t.Fatalf("old completion removed current provider placeholder: %q", got)
	}
	if old := u.agents.Entries[0].Source; old.Text != "Wait ended" || old.Native.Phase != "turn/completed" {
		t.Fatalf("old completion did not settle its wait: %+v", old)
	}
	*at = at.Add(2 * time.Second)
	assertAgentWork(t, u, "child", *at, 2*time.Second, true)
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "current", "itemId": "summary", "delta": "First"})
	reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "current", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "Current answer"}})
	reasoningTestNotify(t, u, "turn/completed", oldEnd)
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "current", "itemId": "summary", "delta": " continued"})
	reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "current", "status": "completed"}})
	var summary, final bool
	for _, entry := range u.agents.Entries {
		if entry.Source.Kind == "reasoning" {
			summary = entry.Source.Text == "First continued" && entry.Source.Native.Phase == "item/completed" && entry.Source.Native.Thought == 5*time.Second
		}
		final = final || entry.Source.Kind == "final" && entry.Source.Text == "Current answer"
	}
	if !summary || !final {
		t.Fatalf("matching turn did not settle its summary and final: summary=%v final=%v entries=%+v", summary, final, u.agents.Entries)
	}
	assertAgentWork(t, u, "child", *at, 2*time.Second, false)
	activity.BeginResponse("child", true, time.Now())
	u.applyObservedActivity()
	reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "without-start", "status": "interrupted"}})
	if got := ansi.Strip(strings.Join(u.agents.RenderFeed(90, 40).Lines, "\n")); strings.Contains(got, "Thinking") {
		t.Fatalf("completion without a start left provider thinking: %q", got)
	}
}

func TestAppServerClosedTurnRejectsLateStreaming(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u, _ := newActiveWorkTestUI(t)
			u.agents.setDisplayFilter(false, "/root/worker")
			delta := map[string]any{"threadId": thread, "turnId": "without-start", "itemId": "summary", "delta": "Initial"}
			reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", delta)
			key := [2]string{thread, "without-start"}
			if u.session.Transcript(key[0], key[1]).Turn == nil {
				t.Fatal("first item did not establish its turn scope")
			}
			reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "without-start", "status": "interrupted"}})
			delta["delta"] = " leaked"
			reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", delta)
			reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "without-start", "item": map[string]any{"id": "late", "type": "reasoning", "summary": []string{"leaked"}}})
			reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "without-start"}})
			if u.session.Transcript(key[0], key[1]).Turn != nil || u.session.agent(u.session.path(thread)).Responding || u.Turn == "without-start" {
				t.Fatal("late observations revived a closed turn")
			}
			view := u.view
			if thread == "child" {
				view = u.agents
			}
			if got := ansi.Strip(strings.Join(view.RenderFeed(90, 40).Lines, "\n")); !strings.Contains(got, "Initial") || strings.Contains(got, "leaked") {
				t.Fatalf("closed-turn content changed: %q", got)
			}
		})
	}
}

func TestAppServerClosedCompletionKeepsCurrentTurnSelection(t *testing.T) {
	u, _ := newActiveWorkTestUI(t)
	u.agents.setDisplayFilter(false, "/root/worker")
	reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "old", "status": "completed"}})
	activity := sessioncontrol.NewAudience(nil)
	activity.Attach("main")
	activity.Observe("child", "main", "/root/worker", true)
	setTestAppServerIntegration(u, &observationFixture{activity: activity})
	activity.BeginResponse("child", true, u.now())
	u.applyObservedActivity()
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "current"}})
	reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "current", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "Current answer"}})
	for _, item := range []binding.ThreadItem{{ID: "late-summary", Type: "reasoning", Summary: []string{"Older summary"}}, {ID: "late-answer", Type: "agentMessage", Text: "Older answer"}} {
		reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "old", "item": item})
	}
	if got := ansi.Strip(strings.Join(u.agents.RenderFeed(90, 40).Lines, "\n")); !strings.Contains(got, "Thinking") || !strings.Contains(got, "Older summary") || !strings.Contains(got, "Older answer") {
		t.Fatalf("late completion lost content or consumed current thinking: %q", got)
	}
	if u.session.Transcript("child", "old").Turn != nil {
		t.Fatal("late completion recreated correlation")
	}
	reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "current", "status": "completed"}})
	var finals []string
	for _, entry := range u.agents.Entries {
		if entry.Source.Kind == "final" {
			finals = append(finals, entry.Source.Text)
		}
	}
	if !slices.Equal(finals, []string{"Current answer"}) {
		t.Fatalf("late completion replaced current answer selection: %q", finals)
	}
}

func TestAppServerRestorePublicSummaries(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	var turns []binding.HistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"t","status":"completed","items":[{"id":"r","type":"reasoning","summary":["Public restored summary."],"content":["PRIVATE"]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	u.session.registerThread(binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	u.restoreActivityThread(binding.ThreadInfo{ID: "child", Turns: turns}, false)
	u.agents.setDisplayFilter(false, "/root/worker")
	for _, view := range []*activityAdapter{u.view, u.agents} {
		feed := view.RenderFeed(90, 40)
		got := ansi.Strip(strings.Join(feed.Lines, "\n"))
		if !strings.Contains(got, "• Public restored summary.") || strings.Count(got, "Public restored summary.") != 1 || strings.Contains(got, "PRIVATE") {
			t.Fatalf("restored summary did not fold: %q", got)
		}
		row := slices.IndexFunc(feed.Snippets, func(s activityui.Snippet) bool { return s != activityui.Snippet{} })
		if row >= 0 {
			t.Fatal("fully shown restored reasoning has a redundant detail target")
		}
	}
	// A new turn must not restart the restored summary animation.
	agent := *u.session.agent("/root/worker")
	agent.Responding = true
	first, _ := u.agents.testCurrent(agent, time.Unix(0, 0))
	second, _ := u.agents.testCurrent(agent, time.Unix(1, 0))
	if first != second {
		t.Fatal("restored reasoning animated in a later turn")
	}
	// Raw-only items are decodable but produce no visible entry.
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"raw","type":"reasoning","summary":[],"content":["PRIVATE"]}}}`)
	if len(u.view.Entries) != 1 {
		t.Fatalf("raw reasoning reached transcript: %+v", u.view.Entries)
	}
}

func TestAppServerComposerKeepsWorkingDuringReasoning(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "**Checking layout**"})
	for _, dock := range []int{0, 3} {
		frame, dockRect := u.testMainFrame(70, 12, dock)
		border := u.mainView.ComposerRect.Y - 1
		if len(frame) != 12 || !strings.Contains(ansi.Strip(frame[border]), "◐ Working") || strings.Contains(ansi.Strip(frame[border]), "Checking layout") || dockRect.Y+dockRect.H != border-1 || frame[border-1] != "" {
			t.Fatalf("reasoning replaced Working: dock=%d frame=%q", dock, frame)
		}
	}
	for _, size := range [][2]int{{1, 1}, {7, 3}, {12, 4}} {
		frame, _ := u.testMainFrame(size[0], size[1], 0)
		if len(frame) != size[1] {
			t.Fatalf("height overflow: %q", frame)
		}
		for _, row := range frame[u.mainView.ComposerRect.Y:] {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("width overflow: %q", row)
			}
		}
	}
}

func TestAppServerWorkingShimmers(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	a, b := u.sessionLabel(u.sessionPresentation().Status.Started), u.sessionLabel(u.sessionPresentation().Status.Started.Add(500*time.Millisecond))
	if a == b || strings.TrimSuffix(ansi.Strip(a), " 0s") != strings.TrimSuffix(ansi.Strip(b), " 500ms") || !strings.Contains(ansi.Strip(a), "Working") {
		t.Fatalf("no text-preserving shimmer: %q %q", a, b)
	}
	u.Turn = ""
	if u.sessionLabel(u.sessionPresentation().Status.Started) != u.sessionLabel(u.sessionPresentation().Status.Started.Add(500*time.Millisecond)) {
		t.Fatal("idle label animates")
	}
}

func TestAppServerCompletedElapsedTime(t *testing.T) {
	for seconds, elapsed := range map[int]string{0: "0s", 12: "12s", 60: "1m", 75: "1m15s", 82: "1m22s", 3600: "1h", 3682: "1h1m22s"} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
		u.sessionPresentation().Status.Started = time.Now().Add(-time.Duration(seconds) * time.Second)
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
	u.Text = "one\ntwo\nthree\nfour"
	reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	u.shell.preview(previewmodel.Preview{ID: "p", Caller: "/root", Status: previewmodel.PreviewEdit, Input: "package a\n"}, false)
	reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "**Active summary**"})
	var out bytes.Buffer
	if err := u.testPaint(&out, 80, 14); err != nil {
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
			u.Thread = ""
			if status == "Sending…" {
				u.Thread = "main"
				u.Submit("pending", "")
			}
			if status == "Interrupting…" {
				u.Thread, u.Turn = "main", "turn"
			}
			if strings.HasPrefix(status, "Restoring") {
				u.Thread = "main"
				if err := u.restorePaneContent(binding.ThreadInfo{ID: "main"}); err != nil {
					t.Fatal(err)
				}
			}
			u.sessionPresentation().Status.Set(status, false)
			start := time.Unix(0, 0)
			dim, bright, dimAgain := u.sessionLabel(start), u.sessionLabel(start.Add(time.Second)), u.sessionLabel(start.Add(2*time.Second))
			if !u.publishPresentation(u.now()).Status.Animating || dim == bright || dim != dimAgain || ansi.Strip(dim) != ansi.Strip(bright) {
				t.Fatalf("pending status does not pulse: %q %q %q", dim, bright, dimAgain)
			}
			u.sessionPresentation().Status.Alert = true
			if u.publishPresentation(u.now()).Status.Animating || u.sessionLabel(start) != u.sessionLabel(start.Add(time.Second)) {
				t.Fatal("alert animates")
			}
			u.restoring = nil
			u.LeaveThread()
			u.Thread = "main"
			u.sessionPresentation().Status.Set("Ready", false)
			if u.publishPresentation(u.now()).Status.Animating || u.sessionLabel(start) != u.sessionLabel(start.Add(time.Second)) {
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
	u.agents.setDisplayFilter(false, "/root/worker")
	feed := func() string { return ansi.Strip(strings.Join(u.agents.RenderFeed(90, 40).Lines, "\n")) }
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
	u.agents.setDisplayFilter(false, "/root/worker")
	body := "**Reviewing**\n\nAlpha.\n\nBravo.\n\nCharlie.\n\nDelta."
	for _, thread := range []string{"main", "child"} {
		reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": body})
		reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{body}}})
		nextEvent(t, u, thread)
	}
	settleActivity(time.Now().Add(time.Minute), u.view, u.agents)
	for name, view := range map[string]*activityAdapter{"Main": u.view, "Activity": u.agents} {
		t.Run(name, func(t *testing.T) {
			render := func() activityui.Feed { return view.RenderFeed(90, 40) }
			plain := func(feed activityui.Feed) string { return ansi.Strip(strings.Join(feed.Lines, "\n")) }
			feed := render()
			if got := plain(feed); !strings.Contains(got, "• Reviewing") || strings.Contains(got, "Alpha.") {
				t.Fatalf("finished thinking did not fold: %q", got)
			}
			row := slices.IndexFunc(feed.Snippets, func(s activityui.Snippet) bool { return s != activityui.Snippet{} })
			if row < 0 || !strings.Contains(ansi.Strip(feed.Lines[row]), "Reviewing") {
				t.Fatal("folded thinking cannot expand")
			}
			if !u.shell.openOutput(view, feed.Snippets[row]) {
				t.Fatal("folded thinking did not request dialog")
			}
			page := view.Painter.DetailDocument(u.shell.output.pages[0].DetailPage, nil, nil, 80)
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
		entry := sessionpresentation.ActivityEntry{Kind: "reasoning", Text: "Body.", Native: &sessionpresentation.NativeItem{Phase: "item/completed", Thought: thought}}
		block := parseLiveActivity(entry)[0]
		got := ""
		if block.Thought >= time.Second {
			got = activityui.FormatDuration(block.Thought)
		}
		if got != want {
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
	u.view.Conversation = true
	u.view.testAudience()
	activity := sessioncontrol.NewAudience(nil)
	activity.Attach("main")
	activity.Observe("child", "main", "/root/worker", true)
	setTestAppServerIntegration(u, &observationFixture{activity: activity})
	reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	u.agents.setDisplayFilter(false, "/root/worker")
	feed := func(view *activityAdapter) string {
		return ansi.Strip(strings.Join(view.RenderFeed(90, 40).Lines, "\n"))
	}
	start := func(thread string, ago time.Duration) {
		t.Helper()
		activity.BeginResponse(thread, true, time.Now().Add(-ago))
		u.applyObservedActivity()
	}
	thinkingRows := func(view *activityAdapter) int {
		rows := 0
		for _, entry := range view.Entries {
			if entry.Source.Kind == "reasoning" {
				rows++
			}
		}
		return rows
	}

	// Main and child each show the block while waiting for the first delta.
	start("main", 3*time.Second)
	start("child", 3*time.Second)
	for name, view := range map[string]*activityAdapter{"Main": u.view, "Activity": u.agents} {
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
	activity.BeginResponse("main", false, time.Now())
	if starts := activity.TakeStarts("main"); len(starts) != 0 {
		t.Fatalf("Codex-summarized request started a thinking block: %+v", starts)
	}
	activity.Observe("other", "", "/root", false)
	activity.BeginResponse("other", true, time.Now())
	if starts := activity.TakeStarts("main"); len(starts) != 0 || len(activity.TakeStarts("main")) != 0 {
		t.Fatalf("request start crossed roots: %+v", starts)
	}
}

func TestAppServerDuplicateSummaryCompletionKeepsNextThinking(t *testing.T) {
	for _, state := range []string{"main/painted", "child/painted", "main/draining", "child/draining"} {
		for _, completion := range []struct{ name, text string }{{"refresh", "Updated summary."}, {"empty", ""}} {
			t.Run(state+"/"+completion.name, func(t *testing.T) {
				thread, reveal, _ := strings.Cut(state, "/")
				u, at := newActiveWorkTestUI(t)
				activity := sessioncontrol.NewAudience(nil)
				activity.Attach("main")
				activity.Observe("child", "main", "/root/worker", true)
				setTestAppServerIntegration(u, &observationFixture{activity: activity})
				u.agents.setDisplayFilter(false, "/root/worker")
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				start := func() {
					activity.BeginResponse(thread, true, *at)
					u.applyObservedActivity()
				}
				complete := func(id, text string) {
					reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": binding.ThreadItem{ID: id, Type: "reasoning", Summary: []string{text}}})
				}
				reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
				start()
				*at = at.Add(3 * time.Second)
				completedAt := *at
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": binding.ThreadItem{ID: "first", Type: "reasoning", Summary: []string{"Original summary."}}})
				if reveal == "painted" {
					rollCommandOutput(u)
					if got := view.Entries[0].Source; got.Native.Thought != 3*time.Second {
						t.Fatalf("completion-only summary lost provider timing: %+v", got.Native)
					}
				}
				*at = at.Add(2 * time.Second)
				start()
				*at = at.Add(time.Second)
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": binding.ThreadItem{ID: "first", Type: "reasoning", Summary: []string{completion.text}}})
				if got := view.Entries[0].Source; reveal == "painted" && (got.Native.Phase != "item/completed" || got.Text != "Original summary.") {
					t.Errorf("duplicate completion replayed a finished summary: phase=%s text=%q", got.Native.Phase, got.Text)
				}
				rollCommandOutput(u)
				paintedAt := completedAt
				if reveal == "draining" {
					paintedAt = *at
				}
				want := completion.text
				if want == "" {
					want = "Original summary."
				}
				if got := view.Entries[0].Source; got.Text != want || got.Native.Thought != 3*time.Second || got.Observed != completedAt || got.Completed != paintedAt {
					t.Errorf("refreshed completion lost content or original timing: text=%q thought=%v observed=%v completed=%v", got.Text, got.Native.Thought, got.Observed, got.Completed)
				}
				if got := ansi.Strip(strings.Join(view.RenderFeed(90, 40).Lines, "\n")); !strings.Contains(got, strings.TrimSuffix(want, ".")) || !strings.Contains(got, "Thinking") {
					t.Fatalf("duplicate completion removed next provider thinking: %q", got)
				}
				*at = at.Add(4 * time.Second)
				complete("next", "Next summary.")
				if len(view.Entries) != 2 || view.Entries[1].Source.Native.Item != "next" || view.Entries[1].Source.Native.Thought != 5*time.Second {
					t.Fatalf("next completion lost provider correlation: %+v", view.Entries)
				}
				if got := ansi.Strip(strings.Join(view.RenderFeed(90, 40).Lines, "\n")); strings.Contains(got, "Thinking") || !strings.Contains(got, "Next summary") {
					t.Fatalf("next completion left orphan thinking: %q", got)
				}
			})
		}
	}
}

func TestAppServerStartedSummarySettlesWithoutDelta(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "interrupted"}[interrupted], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.agents.setDisplayFilter(false, "/root/worker")
			for _, thread := range []string{"main", "child"} {
				reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
				reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{"**Checking**\n\nStarted public body."}}})
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				if got := ansi.Strip(strings.Join(view.RenderFeed(90, 40).Lines, "\n")); strings.Contains(got, "Thinking…") || !strings.Contains(got, "• Checking") || !strings.Contains(got, "Started public body.") {
					t.Fatalf("started summary is not streaming: %s", got)
				}
				if interrupted {
					reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "interrupted"}})
				} else {
					reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{}}})
				}
				nextEvent(t, u, thread)
				settleActivity(time.Now().Add(time.Minute), view)
				feed := view.RenderFeed(90, 40)
				if got := ansi.Strip(strings.Join(feed.Lines, "\n")); strings.Contains(got, "Thinking…") || !strings.Contains(got, "• Checking") || strings.Contains(got, "Started public body.") {
					t.Fatalf("started summary did not settle: %s", got)
				}
				row := slices.IndexFunc(feed.Snippets, func(s activityui.Snippet) bool { return s != activityui.Snippet{} })
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
			u.view.Clock, u.agents.Clock = u.clock, u.clock
			u.view.Painter.Theme, u.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
			reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.agents.setDisplayFilter(false, "/root/worker")
			view := u.view
			if thread == "child" {
				view = u.agents
			}
			reasoningTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
			reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{}}})
			frame := func() string { return ansi.Strip(strings.Join(view.RenderFeed(80, 40).Lines, "\n")) + "\n" }
			t.Run("waiting", func(t *testing.T) {
				uisnapshot.Assert(t, "testdata/snapshots/reasoning-waiting-"+thread+".txt", frame())
			})
			// Public summary can arrive only just before completion. Its
			// arrival is not the beginning of the reasoning item.
			at = at.Add(4200 * time.Millisecond)
			reasoningTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": "**Checking**\n\nPublic summary."})
			at = at.Add(83 * time.Millisecond)
			reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{"**Checking**\n\nPublic summary."}}})
			if got := frame(); strings.Count(got, "Checking for 4s") != 1 {
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
			{"sections", "**Finalizing execution checks**\n\nFirst retained paragraph.\n\n**Preparing final renderings**\n\nLast retained paragraph."},
		} {
			t.Run(thread+"_"+tc.name, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
				u.clock = func() time.Time { return at }
				u.view.Clock, u.agents.Clock = u.clock, u.clock
				u.view.Painter.Theme, u.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
				reasoningTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.agents.setDisplayFilter(false, "/root/worker")
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
						uisnapshot.Assert(t, "testdata/snapshots/reasoning-lifecycle-"+thread+"-"+tc.name+"-"+stage+".txt", strings.Join(view.RenderFeed(80, 40).Lines, "\n")+"\n")
					})
				}
				snapshot("streaming")
				at = at.Add(2 * time.Second)
				// Completion may omit text already delivered in deltas.
				reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning"}})
				snapshot("done")
				reasoningTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
				nextEvent(t, u, thread)
				if len(view.Entries) != 2 {
					t.Fatalf("reasoning completion retained live state or duplicated entries: %+v", view.Entries)
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
		got := ansi.Strip(strings.Join(u.view.RenderFeed(80, 40).Lines, "\n"))
		if strings.Contains(got, "Thinking") || strings.Contains(got, "Thought") || strings.Contains(got, "PRIVATE") {
			t.Fatalf("empty reasoning left visible or unfinished state: %s", got)
		}
	}
}
