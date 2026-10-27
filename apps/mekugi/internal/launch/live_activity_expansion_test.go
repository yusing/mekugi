package launch

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func expansionFixture(main bool) *activityAdapter {
	v := newActivityAdapter()
	v.Conversation, v.ChildrenOnly = main, !main
	v.testAudience()
	v.Painter.Theme = diffrender.DarkTheme
	now := time.Date(2026, 9, 30, 22, 31, 0, 0, time.Local)
	v.Clock = func() time.Time { return now }
	agent := "/root/worker"
	if main {
		agent = "Main"
	}
	narrative := strings.Repeat("An event with retained details.\n", 8)
	v.appendEntry(sessionpresentation.ActivityEntry{Seq: 1, Agent: agent, Kind: "text", Text: narrative, Observed: now}, []activityui.Block{{Kind: "text", Body: narrative}})
	v.appendEntry(sessionpresentation.ActivityEntry{Seq: 2, Agent: agent, Kind: "reasoning", Observed: now}, []activityui.Block{{Kind: "summary", Label: "Inspecting", Body: "**Inspecting**\n\n" + strings.Repeat("Public reasoning detail.\n\n", 6), Live: true}})
	v.appendEntry(sessionpresentation.ActivityEntry{Seq: 3, Agent: agent, Kind: "tool", Text: "Run printf", Observed: now}, []activityui.Block{{Kind: "op", Verb: "Run", Label: "printf", Code: strings.Repeat("echo retained-source\n", 8), Lang: "bash", Fenced: true, Tail: []string{"first output", "last output"}}})
	v.LastSeq = 3
	v.publishActivity()
	// Disclosure fixtures belong to the renderer, independently of source facts.
	v.Disclosure.Collapse(2, 0)
	v.Disclosure.Collapse(3, 0)
	return v
}

func TestUISnapshotLiveActivityExpansion(t *testing.T) {
	for _, main := range []bool{false, true} {
		v := expansionFixture(main)
		for _, state := range []string{"events", "all", "default"} {
			v.ToggleExpansion()
			feed := v.RenderFeed(80, 150)
			rows := v.Viewport(feed, 150)[:len(feed.Lines)]
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/expansion-%t-%s.txt", main, state), strings.Join(rows, "\n"))
		}
	}
}

func TestNativeUIExpansionFocus(t *testing.T) {
	u, _ := newAppServerTestUI()

	u.Text = "Keep this draft"
	for _, focus := range []int{0, 2} {
		terminalTest(u).Focus = 0
		u.testActivity(u.shell.Agents).setDisplayFilter(focus == 2, "/root/worker")
		view := u.testActivity(u.shell.Transcript)
		if focus != 0 {
			view = u.testActivity(u.shell.Agents)
		}
		before := view.Expansion
		if err := terminalTest(u).key(5); err != nil {
			t.Fatal(err)
		}
		if u.Text != "Keep this draft" || terminalTest(u).Focus != 0 || view.Expansion != (before+1)%3 {
			t.Fatal("Ctrl+E changed the draft, focus or wrong pane")
		}
		if !strings.Contains(ansi.Strip(terminalTest(u).nativeStatus()), "^B 1-3 views") {
			t.Fatal("pane hint is incorrect")
		}
	}
	playback := replayPlaybackTestNew(t)
	for _, focus := range []int{0, 2} {
		playback.ui.shell.Focus = 0
		playback.ui.testActivity(playback.ui.agents).setDisplayFilter(focus == 2, "/root/worker")
		view := playback.ui.testActivity(playback.ui.view)
		if focus != 0 {
			view = playback.ui.testActivity(playback.ui.agents)
		}
		before := view.Expansion
		if _, err := playback.key(5); err != nil {
			t.Fatal(err)
		}
		if view.Expansion != (before+1)%3 {
			t.Fatal("replay ignored the focused pane shortcut")
		}
	}
}

func TestLiveActivityExpansionViewportCache(t *testing.T) {
	lexer := registerViewportSyntaxCountingLexer(t)
	for _, main := range []bool{false, true} {
		clear(lexer.sources)
		v := viewportSyntaxView(main, "viewportfixture")
		const skillSource = "package offscreenSkill"
		v.replaceEntry(1, v.Entries[1].Source, append(v.blocks(1), activityui.Block{
			Kind: "op", Verb: "Attached skill", Label: "viewport",
			Tail: strings.Split(strings.Repeat("Skill detail.\n\n", 20)+"```viewportfixture\n"+skillSource+"\n```", "\n"),
		}))
		first := strings.ReplaceAll(v.blocks(1)[0].Code, "\t", "    ")
		v.Disclosure.Collapse(v.Entries[1].Source.Seq, len(v.Entries[1].Values)-1)
		for range 2 {
			v.ToggleExpansion()
			v.Viewport(v.RenderFeed(70, 9), 9)
			if lexer.sources[first] != 0 || lexer.sources[skillSource] != 0 {
				t.Fatal("expansion decorated an out-of-view operation or Markdown skill")
			}
		}
		v.ToggleExpansion()
		v.ToggleExpansion()
		v.Following, v.Offset = false, 10
		v.Viewport(v.RenderFeed(70, 9), 9)
		var anchor activityui.Span
		for _, span := range v.FeedSpans {
			if span.End > v.Offset {
				anchor = span
				break
			}
		}
		saved := maps.Clone(v.Runs)
		v.ToggleExpansion()
		v.Viewport(v.RenderFeed(70, 9), 9)
		if v.Following {
			t.Fatal("toggle resumed a paused transcript")
		}
		for _, span := range v.FeedSpans {
			if span.Seq == anchor.Seq && (v.Offset < span.Start || v.Offset >= span.End) {
				t.Fatal("toggle lost the top scrollback item")
			}
		}
		v.ToggleExpansion()
		v.Viewport(v.RenderFeed(70, 9), 9)
		for key, run := range saved {
			if key.Expansion == 1 && len(run.Lines) > 0 && &run.Lines[0] != &v.Runs[key].Lines[0] {
				t.Fatal("repeat toggle rebuilt a cached layout")
			}
		}
		v.Entries[1].Values[0].Code = "package updated\nfunc updated() int { return 42 }\n"
		v.invalidateEntry(2)
		v.Following, v.Offset = false, 0
		v.Viewport(v.RenderFeed(70, 9), 9)
		v.ToggleExpansion()
		compact := v.RenderFeed(70, 9)
		if strings.Contains(ansi.Strip(strings.Join(compact.Lines, "\n")), "package source0") {
			t.Fatal("toggle restored stale source after an update")
		}
		v.ToggleExpansion()
		v.Viewport(v.RenderFeed(70, 50), 50)
		if lexer.sources[skillSource] == 0 {
			t.Fatal("expanded Markdown skill entering the viewport was not decorated")
		}
	}
}

func TestAppServerExpansionRetainedReads(t *testing.T) {
	for _, main := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.shell.Transcript.Conversation = main
		u.testActivity(u.shell.Transcript).testAudience()
		read := func(id string) {
			appServerTestNotify(t, u, "item/completed", map[string]any{
				"threadId": "main", "turnId": "t", "item": map[string]any{
					"id": id, "type": "commandExecution", "command": "cat " + id + ".go",
					"status": "completed", "exitCode": 0, "aggregatedOutput": id + "-start\n" + strings.Repeat("retained line\n", 20) + id + "-end\n",
				},
			})
		}
		read("first")
		read("second")
		render := func() string {
			return ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 100).Lines, "\n"))
		}
		if strings.Contains(render(), "first-start") {
			t.Fatal("read output must start compact")
		}
		if _, err := u.key("\x05"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(render(), "first-start") {
			t.Fatal("expanded events opened command output")
		}
		if _, err := u.key("\x05"); err != nil {
			t.Fatal(err)
		}
		text := render()
		for _, detail := range []string{"first-start", "first-end", "second-start", "second-end"} {
			if !strings.Contains(text, detail) {
				t.Fatalf("Main=%t: merged read omitted retained detail %q", main, detail)
			}
		}
		read("third")
		if !strings.Contains(render(), "third-start") {
			t.Fatal("arriving reads ignored pane expansion")
		}
		u.attached.source.Entries[0].Source.Native.Output.Release()
		u.testActivity(u.shell.Transcript).invalidateEntry(u.attached.source.Entries[0].Source.Seq)
		if !strings.Contains(render(), "first-end") {
			t.Fatal("released output erased the retained inline tail")
		}
		if _, err := u.key("\x05"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(render(), "third-start") {
			t.Fatal("collapse all retained expanded read content")
		}
	}
}

func TestLiveActivityExpansionReusesUserPrompt(t *testing.T) {
	v := newActivityAdapter()
	v.Conversation = true
	v.testAudience()
	v.Clock = func() time.Time { return time.Date(2026, 9, 30, 22, 31, 0, 0, time.Local) }
	v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "You", Kind: "text", Text: "Keep this prompt and its wrapped source.\n\n```go\npackage example\n```", Observed: time.Date(2026, 9, 30, 22, 31, 0, 0, time.Local)},
		{Seq: 2, Agent: "Main", Kind: "text", Text: strings.Repeat("An expandable event.\n", 8), Observed: time.Date(2026, 9, 30, 22, 31, 0, 0, time.Local)},
	}})
	v.Passed = map[uint64]bool{1: true}
	v.Viewport(v.RenderFeed(70, 30), 30)
	key, original := liveActivityCacheRun(t, v, 1)
	for range 3 {
		v.ToggleExpansion()
		v.Viewport(v.RenderFeed(70, 30), 30)
		cached, ok := v.Runs[key]
		if !ok || &cached.Lines[0] != &original.Lines[0] {
			t.Fatal("expansion rebuilt an unchanged user prompt")
		}
		for other := range v.Runs {
			if other.First == 1 && other != key {
				t.Fatal("expansion retained a duplicate user-prompt layout")
			}
		}
	}
}

func TestUISnapshotExpansionDefaultPastBatches(t *testing.T) {
	v := sharedEventsView(false)
	appendTool := func(seq uint64, verb string) {
		v.appendEntry(sessionpresentation.ActivityEntry{Seq: seq, Agent: "Main", Kind: "tool", Text: verb,
			Native: &sessionpresentation.NativeItem{Live: true, Status: "completed"}}, []activityui.Block{{Kind: "op", Verb: verb, Label: "target"}})
	}
	appendTool(1, "Read")
	appendTool(2, "Inspect")
	v.appendEntry(sessionpresentation.ActivityEntry{Seq: 3, Agent: "Main", Kind: "question", Text: "Asked", Native: &sessionpresentation.NativeItem{Live: true}},
		[]activityui.Block{{Kind: "op", Verb: "Ask", Questions: []activityui.Question{{Text: "Continue?", State: "answered", Answer: "Yes"}}}})
	appendTool(4, "Search")
	appendTool(5, "Inspect")
	v.appendEntry(sessionpresentation.ActivityEntry{Seq: 6, Agent: "Main", Kind: "text", Text: strings.Repeat("Later visible event.\n", 10)},
		[]activityui.Block{{Kind: "text", Body: strings.Repeat("Later visible event.\n", 10)}})
	v.Viewport(v.RenderFeed(80, 4), 4)
	appendTool(7, "Read")
	appendTool(8, "Inspect")
	for range 3 {
		v.Viewport(v.RenderFeed(80, 40), 40)
		v.ToggleExpansion()
	}
	feed := v.RenderFeed(80, 40)
	for _, seq := range []uint64{1, 4, 7} {
		key, run := liveActivityCacheRun(t, v, seq)
		// Select the displayed variant, since the other two remain warm.
		for candidate, cached := range v.Runs {
			if candidate.First == seq && candidate.Expansion == 0 {
				key, run = candidate, cached
			}
		}
		if folded := len(run.Blocks) == 1 && run.Blocks[0].Kind == "batch"; folded != (seq < 7) {
			t.Fatalf("default fold at %d = %t (key %+v)", seq, folded, key)
		}
	}
	uisnapshot.Assert(t, "testdata/snapshots/expansion-default-past-batches.txt", strings.Join(v.Viewport(feed, 40), "\n"))
}

func BenchmarkLiveActivityExpansion(b *testing.B) {
	for _, main := range []bool{false, true} {
		for _, action := range []string{"toggle", "scroll", "cold-toggle"} {
			b.Run(fmt.Sprintf("Main=%t/%s", main, action), func(b *testing.B) {
				v := viewportSyntaxView(main, "go")
				prefix := append([]sessionpresentation.ActivityRecord(nil), v.Entries...)
				for range 39 {
					for _, record := range prefix {
						entry := record.Source
						entry.Seq = uint64(len(v.Entries) + 1)
						v.appendEntry(entry, v.Disclosure.Blocks(record.Values))
					}
				}
				for range 3 {
					v.ToggleExpansion()
					v.Viewport(v.RenderFeed(100, 30), 30)
				}
				b.ReportAllocs()
				for b.Loop() {
					if action == "scroll" {
						v.Following = false
						v.Offset = (v.Offset + 1) % max(1, v.FeedLines-v.FeedRows)
					} else {
						if action == "cold-toggle" {
							v.Runs = nil
						}
						v.ToggleExpansion()
					}
					v.Viewport(v.RenderFeed(100, 30), 30)
				}
			})
		}
	}
}
