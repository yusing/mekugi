package router

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func expansionFixture(main bool) *liveActivityView {
	v := newLiveActivityView()
	v.conversation, v.childrenOnly = main, !main
	v.painter.Theme = livediff.DarkTheme
	now := journalNoiseTime()
	v.clock = func() time.Time { return now }
	agent := "/root/worker"
	if main {
		agent = "Main"
	}
	narrative := strings.Repeat("An event with retained details.\n", 8)
	v.appendEntry(activityPaneEntry{Seq: 1, Agent: agent, Kind: "text", Text: narrative, Observed: now}, []activityui.Block{{Kind: "text", Body: narrative}})
	v.appendEntry(activityPaneEntry{Seq: 2, Agent: agent, Kind: "reasoning", Observed: now}, []activityui.Block{{Kind: "summary", Label: "Inspecting", Body: "**Inspecting**\n\n" + strings.Repeat("Public reasoning detail.\n\n", 6), Collapsed: true, Live: true}})
	v.appendEntry(activityPaneEntry{Seq: 3, Agent: agent, Kind: "tool", Text: "Run printf", Observed: now}, []activityui.Block{{Kind: "op", Verb: "Run", Label: "printf", Code: strings.Repeat("echo retained-source\n", 8), Lang: "bash", Fenced: true, Tail: []string{"first output", "last output"}, Collapsed: true}})
	event := journalEvent{Op: "set", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", Title: "Check expansion", State: "done", Body: strings.Repeat("Journal body detail. ", 12) + "JOURNAL-END"}}
	v.appendEntry(activityPaneEntry{Seq: 4, Agent: agent, Kind: "journal_event", Text: "Check expansion", Observed: now, journalEvent: &event}, nil)
	v.appendEntry(activityPaneEntry{Seq: 5, Agent: agent, Kind: "journal_card", Observed: now, journalCard: journalNoiseCard()}, nil)
	v.lastSeq = 5
	return v
}

func TestUISnapshotLiveActivityExpansion(t *testing.T) {
	for _, main := range []bool{false, true} {
		v := expansionFixture(main)
		for _, state := range []string{"events", "all", "default"} {
			v.toggleExpansion()
			feed := v.renderFeed(80, 150)
			rows := v.viewport(feed, 150)[:len(feed.lines)]
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/expansion-%t-%s.txt", main, state), strings.Join(rows, "\n"))
		}
	}
}

func TestUISnapshotLiveActivityExpansionChildCompletion(t *testing.T) {
	for _, reportOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("report-only=%t", reportOnly), func(t *testing.T) {
			v := sharedEventsView(false)
			var source strings.Builder
			source.WriteString("Journal result `/root/worker`")
			if !reportOnly {
				writeJournalItems(&source, []journalItem{
					{ID: "first", Text: "First retained answer."},
					{ID: "last", Text: "Last retained answer."},
				})
			}
			source.WriteString("\n\n**Changes:** amber3..amber4\n\nRecorded evaluations (not a net diff):\n\n" +
				indentJournalText("M\t10\t2\tinternal/a.go", "    "))
			entry := activityPaneEntry{Seq: 1, Agent: "/root/worker", Kind: "final", Text: source.String(), Observed: v.now(), activitySeq: 7}
			v.appendEntry(entry, parseLiveActivity(entry))
			for _, mode := range []string{"default", "expanded", "expanded"} {
				feed := v.renderFeed(80, 30)
				v.viewport(feed, 30)
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/expansion-child-report-only-%t-%s.txt", reportOnly, mode),
					append(feed.lines, "plain after completion"), 80)
				v.toggleExpansion()
			}
		})
	}
}

func TestUISnapshotNativeUIExpansionFocus(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.draft = "Keep this draft"
	u.shell.focus = 4
	for range 2 {
		if err := u.shell.key(5); err != nil {
			t.Fatal(err)
		}
	}
	if u.journalView.expansion != 2 {
		t.Fatal("empty Journal pane ignored the expansion choice")
	}
	if err := u.shell.key(5); err != nil {
		t.Fatal(err)
	}
	j := nativeJournalHierarchyFixture(t)
	u.journal = &nativeJournalSink{tree: &j}
	for _, focus := range []int{0, 2, 4} {
		u.shell.focus = focus
		if err := u.shell.key(5); err != nil {
			t.Fatal(err)
		}
		if u.draft != "Keep this draft" || u.shell.focus != focus {
			t.Fatal("Ctrl+E changed the draft or focus")
		}
		if !strings.HasSuffix(ansi.Strip(u.shell.nativeStatus()), "^B 1-5 panes · ^E expand all") {
			t.Fatal("Ctrl+E hint did not follow panes or name the next mode")
		}
	}
	if u.view.expansion != 1 || u.shell.agents.expansion != 1 || u.journalView.expansion != 1 {
		t.Fatal("Ctrl+E did not reach each focused pane")
	}
	for _, state := range []string{"events", "default"} {
		rows := u.journalView.render(&j, 80, 5, false, false, livediff.DarkTheme)
		uisnapshot.Assert(t, "testdata/snapshots/expansion-journal-"+state+".txt", strings.Join(rows, "\n"))
		if state == "events" && len(u.journalView.rows) <= 5 {
			t.Fatal("automatic fitting undid expand all")
		}
		if err := u.shell.key(5); err != nil {
			t.Fatal(err)
		}
		if state == "events" {
			if err := u.shell.key(5); err != nil {
				t.Fatal(err)
			}
		}
	}
	playback := uiReplayPlayback{ui: u}
	for _, focus := range []int{0, 2, 4} {
		u.shell.focus = focus
		if _, err := playback.key(5); err != nil {
			t.Fatal(err)
		}
	}
	if u.view.expansion != 2 || u.shell.agents.expansion != 2 || u.journalView.expansion != 2 {
		t.Fatal("replay ignored the focused pane shortcut")
	}
}

func TestLiveActivityExpansionViewportCache(t *testing.T) {
	lexer := registerViewportSyntaxCountingLexer(t)
	for _, main := range []bool{false, true} {
		clear(lexer.sources)
		v := viewportSyntaxView(main, "viewportfixture")
		const skillSource = "package offscreenSkill"
		v.entries[1].blocks = append(v.entries[1].blocks, activityui.Block{
			Kind: "op", Verb: "Attached skill", Label: "viewport", Collapsed: true,
			Tail: strings.Split(strings.Repeat("Skill detail.\n\n", 20)+"```viewportfixture\n"+skillSource+"\n```", "\n"),
		})
		first := strings.ReplaceAll(v.entries[1].blocks[0].Code, "\t", "    ")
		for range 2 {
			v.toggleExpansion()
			v.viewport(v.renderFeed(70, 9), 9)
			if lexer.sources[first] != 0 || lexer.sources[skillSource] != 0 {
				t.Fatal("expansion decorated an out-of-view operation or Markdown skill")
			}
		}
		v.toggleExpansion()
		v.toggleExpansion()
		v.following, v.offset = false, 10
		v.viewport(v.renderFeed(70, 9), 9)
		var anchor liveActivitySpan
		for _, span := range v.feedSpans {
			if span.end > v.offset {
				anchor = span
				break
			}
		}
		saved := v.runs
		v.toggleExpansion()
		v.viewport(v.renderFeed(70, 9), 9)
		if v.following {
			t.Fatal("toggle resumed a paused transcript")
		}
		for _, span := range v.feedSpans {
			if span.seq == anchor.seq && (v.offset < span.start || v.offset >= span.end) {
				t.Fatal("toggle lost the top scrollback item")
			}
		}
		v.toggleExpansion()
		v.viewport(v.renderFeed(70, 9), 9)
		for key, run := range saved {
			if key.expansion == 1 && len(run.lines) > 0 && &run.lines[0] != &v.runs[key].lines[0] {
				t.Fatal("repeat toggle rebuilt a cached layout")
			}
		}
		v.entries[1].blocks[0].Code = "package updated\nfunc updated() int { return 42 }\n"
		v.invalidateEntry(2)
		v.following, v.offset = false, 0
		v.viewport(v.renderFeed(70, 9), 9)
		v.toggleExpansion()
		compact := v.renderFeed(70, 9)
		if strings.Contains(ansi.Strip(strings.Join(compact.lines, "\n")), "package source0") {
			t.Fatal("toggle restored stale source after an update")
		}
		v.toggleExpansion()
		v.viewport(v.renderFeed(70, 50), 50)
		if lexer.sources[skillSource] == 0 {
			t.Fatal("expanded Markdown skill entering the viewport was not decorated")
		}
	}
}

func TestAppServerExpansionRetainedReads(t *testing.T) {
	for _, main := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.view.conversation = main
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
		render := func() string { return ansi.Strip(strings.Join(u.view.renderFeed(90, 100).lines, "\n")) }
		if strings.Contains(render(), "first-start") {
			t.Fatal("read output must start compact")
		}
		if _, err := u.key(5); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(render(), "first-start") {
			t.Fatal("expanded events opened command output")
		}
		if _, err := u.key(5); err != nil {
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
		u.view.entries[0].native.output.Release()
		u.view.invalidateEntry(u.view.entries[0].Seq)
		if !strings.Contains(render(), "first-end") {
			t.Fatal("released output erased the retained inline tail")
		}
		if _, err := u.key(5); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(render(), "third-start") {
			t.Fatal("collapse all retained expanded read content")
		}
	}
}

func TestLiveActivityExpansionReusesUserPrompt(t *testing.T) {
	v := newLiveActivityView()
	v.conversation = true
	v.clock = journalNoiseTime
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "You", Kind: "text", Text: "Keep this prompt and its wrapped source.\n\n```go\npackage example\n```", Observed: journalNoiseTime()},
		{Seq: 2, Agent: "Main", Kind: "text", Text: strings.Repeat("An expandable event.\n", 8), Observed: journalNoiseTime()},
	}})
	v.passed = map[uint64]bool{1: true}
	v.viewport(v.renderFeed(70, 30), 30)
	key, original := liveActivityCacheRun(t, v, 1)
	for range 3 {
		v.toggleExpansion()
		v.viewport(v.renderFeed(70, 30), 30)
		cached, ok := v.runs[key]
		if !ok || &cached.lines[0] != &original.lines[0] {
			t.Fatal("expansion rebuilt an unchanged user prompt")
		}
		for other := range v.runs {
			if other.first == 1 && other != key {
				t.Fatal("expansion retained a duplicate user-prompt layout")
			}
		}
	}
}

func TestNativeJournalExpansionPreservesSelectionAfterRemoval(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.shell.focus = 4
	j := nativeJournalHierarchyFixture(t)
	u.journal = &nativeJournalSink{tree: &j}
	if err := u.shell.key(5); err != nil {
		t.Fatal(err)
	}
	view := &u.journalView
	if len(view.rows) != 0 {
		t.Fatal("Ctrl+E rebuilt the old Journal layout before its render")
	}
	view.render(&j, 80, 40, true, false, livediff.DarkTheme)
	view.selected = slices.IndexFunc(view.rows, func(row journalPaneRow) bool { return row.node.Path == "/3/1" })
	if view.selected < 0 {
		t.Fatal("fixture lacks the selected descendant")
	}
	j.Items = slices.DeleteFunc(j.Items, func(item journalItem) bool { return item.Path == "/3/1" })
	if err := u.shell.key(5); err != nil {
		t.Fatal(err)
	}
	view.render(&j, 80, 40, true, false, livediff.DarkTheme)
	if view.rows[view.selected].node.Path != "/3" {
		t.Fatal("toggle lost the removed selection's nearest visible ancestor")
	}
}

func TestUISnapshotExpansionDefaultPastBatches(t *testing.T) {
	v := sharedEventsView(false)
	appendTool := func(seq uint64, verb string) {
		v.appendEntry(activityPaneEntry{Seq: seq, Agent: "Main", Kind: "tool", Text: verb,
			native: &liveActivityNativeItem{live: true, status: "completed"}}, []activityui.Block{{Kind: "op", Verb: verb, Label: "target"}})
	}
	appendTool(1, "Read")
	appendTool(2, "Inspect")
	v.appendEntry(activityPaneEntry{Seq: 3, Agent: "Main", Kind: "question", Text: "Asked", native: &liveActivityNativeItem{live: true}},
		[]activityui.Block{{Kind: "op", Verb: "Ask", Questions: []activityui.Question{{Text: "Continue?", State: "answered", Answer: "Yes"}}}})
	appendTool(4, "Search")
	appendTool(5, "Inspect")
	v.appendEntry(activityPaneEntry{Seq: 6, Agent: "Main", Kind: "text", Text: strings.Repeat("Later visible event.\n", 10)},
		[]activityui.Block{{Kind: "text", Body: strings.Repeat("Later visible event.\n", 10)}})
	v.viewport(v.renderFeed(80, 4), 4)
	appendTool(7, "Read")
	appendTool(8, "Inspect")
	for range 3 {
		v.viewport(v.renderFeed(80, 40), 40)
		v.toggleExpansion()
	}
	feed := v.renderFeed(80, 40)
	for _, seq := range []uint64{1, 4, 7} {
		key, run := liveActivityCacheRun(t, v, seq)
		// Select the displayed variant, since the other two remain warm.
		for candidate, cached := range v.runs {
			if candidate.first == seq && candidate.expansion == 0 {
				key, run = candidate, cached
			}
		}
		if folded := len(run.blocks) == 1 && run.blocks[0].Kind == "batch"; folded != (seq < 7) {
			t.Fatalf("default fold at %d = %t (key %+v)", seq, folded, key)
		}
	}
	uisnapshot.Assert(t, "testdata/snapshots/expansion-default-past-batches.txt", strings.Join(v.viewport(feed, 40), "\n"))
}

func BenchmarkLiveActivityExpansion(b *testing.B) {
	for _, main := range []bool{false, true} {
		for _, action := range []string{"toggle", "scroll", "cold-toggle"} {
			b.Run(fmt.Sprintf("Main=%t/%s", main, action), func(b *testing.B) {
				v := viewportSyntaxView(main, "go")
				prefix := append([]liveActivityRecord(nil), v.entries...)
				for range 39 {
					for _, record := range prefix {
						entry := record.activityPaneEntry
						entry.Seq = uint64(len(v.entries) + 1)
						v.appendEntry(entry, record.blocks)
					}
				}
				for range 3 {
					v.toggleExpansion()
					v.viewport(v.renderFeed(100, 30), 30)
				}
				b.ReportAllocs()
				for b.Loop() {
					if action == "scroll" {
						v.following = false
						v.offset = (v.offset + 1) % max(1, v.feedLines-v.feedRows)
					} else {
						if action == "cold-toggle" {
							v.runs = nil
						}
						v.toggleExpansion()
					}
					v.viewport(v.renderFeed(100, 30), 30)
				}
			})
		}
	}
}
