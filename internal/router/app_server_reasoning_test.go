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
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestAppServerPublicSummaryNotifications(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		for _, delta := range []string{"**Checking " + thread + "**", "\n\nPublic " + thread + " summary."} {
			appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "same-id", "delta": delta})
		}
		appServerTestNotify(t, u, "item/reasoning/textDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "same-id", "delta": "PRIVATE"})
	}
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	child := ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, "Public main summary.") || strings.Contains(main, "Public child") || !strings.Contains(child, "Public child summary.") || strings.Contains(child, "Public main") {
		t.Fatalf("summary routing: main=%q, child=%q", main, child)
	}
	for _, thread := range []string{"main", "child"} {
		// Real reasoning content is a string array, unlike user input blocks.
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{
			"id": "same-id", "type": "reasoning", "summary": []string{"**Complete**\n\nFinal public " + thread + "."}, "content": []string{"PRIVATE"}}})
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
	}
	u.agents.only, u.agents.selected = false, "/root/worker"
	main = ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	child = ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if strings.Count(main, "Final public main.") != 1 || strings.Count(child, "Final public child.") != 1 || strings.Contains(main+child, "PRIVATE") || strings.Contains(main+child, "Public main summary") {
		t.Fatalf("completed summaries: main=%q, child=%q", main, child)
	}
	// A reused item ID in a new turn must not replace earlier summaries.
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "next", "itemId": "same-id", "delta": "Next public summary."})
	main = ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, "Final public main.") || !strings.Contains(main, "Next public summary.") {
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
		got := ansi.Strip(strings.Join(view.renderFeed(90, 40).lines, "\n"))
		if strings.Count(got, "Public restored summary.") != 1 || strings.Contains(got, "PRIVATE") {
			t.Fatalf("restored summary: %q", got)
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
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "**Checking layout**"})
	for _, dock := range []int{0, 3} {
		frame, dockRect := u.mainFrame(70, 12, dock)
		border := u.composerRect.y - 1
		if len(frame) != 12 || !strings.Contains(ansi.Strip(frame[border]), "◐ Working") || strings.Contains(ansi.Strip(frame[border]), "Checking layout") || dockRect.y+dockRect.h != border {
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
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
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
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
		u.turnStarted = time.Now().Add(-time.Duration(seconds) * time.Second)
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "completed"}})
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
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	u.shell.preview(diffview.Preview{ID: "p", Caller: "/root", Status: diffview.PreviewEdit, Input: "package a\n"})
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "r", "delta": "**Active summary**"})
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
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t"}})
	u.agents.only, u.agents.selected = false, "/root/worker"
	feed := func() string { return ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n")) }
	for _, delta := range []string{"First ", "thought.\n\nSecond.\n\nThird.\n\nFourth."} {
		appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "r1", "delta": delta})
	}
	if got := feed(); !strings.Contains(got, "• Thinking… · +1 line") || strings.Contains(got, "First thought.") || !strings.Contains(got, "Fourth.") {
		t.Fatalf("live thinking: %q", got)
	}
	// A route without a replayable summary completes with an empty one.
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "r1", "type": "reasoning", "summary": []string{}}})
	if got := feed(); !strings.Contains(got, "• Thought for 0s") || !strings.Contains(got, "First thought.") || strings.Contains(got, "Thinking…") {
		t.Fatalf("completed thinking: %q", got)
	}
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "r2", "delta": "Interrupted thought."})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t", "status": "interrupted"}})
	if got := feed(); strings.Contains(got, "Thinking…") || strings.Count(got, "• Thought for") != 2 || !strings.Contains(got, "Interrupted thought.") {
		t.Fatalf("turn end left thinking live: %q", got)
	}
}

// Finished thinking stays open for a second, then folds to its header in
// Main and Activity; a click toggles it.
func TestAppServerThinkingFolds(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	u.agents.only, u.agents.selected = false, "/root/worker"
	body := "Alpha.\n\nBravo.\n\nCharlie.\n\nDelta."
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": body})
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{body}}})
	}
	for name, view := range map[string]*liveActivityView{"Main": u.view, "Activity": u.agents} {
		t.Run(name, func(t *testing.T) {
			render := func() liveActivityFeed { return view.renderFeed(90, 40) }
			plain := func(feed liveActivityFeed) string { return ansi.Strip(strings.Join(feed.lines, "\n")) }
			if got := plain(render()); !strings.Contains(got, "• Thought for 0s") || !strings.Contains(got, "Delta.") {
				t.Fatalf("thinking folded before its delay: %q", got)
			}
			if view.settle(time.Now()) {
				t.Fatal("fold fired before its delay")
			}
			later := time.Now().Add(activityui.ThinkingLinger)
			if !view.settle(later) || view.settle(later) {
				t.Fatal("fold did not fire exactly once")
			}
			feed := render()
			if got := plain(feed); !strings.Contains(got, "• Thought for 0s") || strings.Contains(got, "Alpha.") {
				t.Fatalf("finished thinking did not fold: %q", got)
			}
			row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool { return s != liveActivitySnippet{} })
			if row < 0 || !strings.Contains(ansi.Strip(feed.lines[row]), "Thought for") {
				t.Fatal("folded thinking cannot expand")
			}
			view.toggleSnippet(feed.snippets[row])
			if got := plain(render()); !strings.Contains(got, "Alpha.") || !strings.Contains(got, "Delta.") {
				t.Fatalf("expanded thinking: %q", got)
			}
			view.toggleSnippet(feed.snippets[row])
			if got := plain(render()); strings.Contains(got, "Alpha.") {
				t.Fatalf("thinking did not fold again: %q", got)
			}
		})
	}
}
