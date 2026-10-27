package launch

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/diffrender"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Compare the complete feed and hit targets, not just the visible text: an
// unchanged rendering must not retain stale snippets or question navigation.
func assertLiveActivityCacheFresh(t *testing.T, v *activityAdapter) {
	t.Helper()
	got := v.RenderFeed(90, 24)
	rows := maps.Clone(v.QuestionRows)
	fresh := *v
	freshView := *v.View
	fresh.View = &freshView
	fresh.Runs = nil
	fresh.QuestionRows = nil
	want := fresh.RenderFeed(90, 24)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(rows, fresh.QuestionRows) {
		t.Fatalf("cached feed differs from fresh render:\ngot: %#v\nwant: %#v\nrows: %v / %v", got, want, rows, fresh.QuestionRows)
	}
	for key, run := range v.Runs {
		// Private render-pass accounting is intentionally different. Compare
		// the cached rendering facts rather than renderer bookkeeping.
		facts := func(r activityui.Run) activityui.Run {
			return activityui.Run{Colored: r.Colored, Lines: r.Lines, Blocks: r.Blocks, Snippets: r.Snippets, Questions: r.Questions, EntryRows: r.EntryRows, Batch: r.Batch}
		}
		if !reflect.DeepEqual(facts(run), facts(fresh.Runs[key])) {
			t.Fatalf("cached run %v has stale lines, snippets, questions, blocks or entryRows", key)
		}
	}
}

func liveActivityCacheRun(t *testing.T, v *activityAdapter, seq uint64) (activityui.RunKey, activityui.Run) {
	t.Helper()
	for key, run := range v.Runs {
		if key.First <= seq && seq <= key.Last {
			return key, run
		}
	}
	t.Fatalf("no cached run containing entry %d", seq)
	return activityui.RunKey{}, activityui.Run{}
}

func TestLiveActivityCacheEntryUpdates(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, change := range []string{"output delta", "reasoning", "exit", "receipt", "paced reveal", "settled output"} {
		t.Run(change, func(t *testing.T) {
			v := newActivityAdapter()
			v.ChildrenOnly = true
			v.testAudience()
			v.Painter.Theme = diffrender.DarkTheme
			v.Clock = func() time.Time { return now }
			entry := sessionpresentation.ActivityEntry{Seq: 3, Agent: "/root/working", Kind: "tool", CallID: "command", Text: "Run `printf hello`", Observed: now,
				Native:     &sessionpresentation.NativeItem{Thread: "child", Turn: "turn", Item: "command", Phase: "item/commandExecution/outputDelta"},
				OutputTail: []string{"hello"}}
			switch change {
			case "reasoning":
				entry.Kind, entry.Text, entry.CallID = "reasoning", "**Checking the result**\nInitial public summary", "summary"
			case "paced reveal":
				entry.Text = "List `.`\n\nSearch `needle`\n\nRead `a.go`"
				entry.Native.Live = true
			case "settled output":
				entry.Native.Phase, entry.Completed = "item/completed", now
				// A one-line result is already compact and is not collapsible.
				entry.OutputTail = []string{"hello", "completed second output line"}
			}
			entries := []sessionpresentation.ActivityEntry{
				{Seq: 1, Agent: "/root/finished", Kind: "text", Text: "Completed independent agent work", Observed: now},
				{Seq: 2, Agent: "/root/other", Kind: "text", Text: "Another independent result", Observed: now}, entry,
			}
			if change == "settled output" {
				entries = append(entries, sessionpresentation.ActivityEntry{Seq: 4, Agent: entry.Agent, Kind: "text", Text: "The next event settles output", Observed: now})
			}
			v.Apply(sessionpresentation.ActivityEvent{Entries: entries})
			v.RenderFeed(90, 24)
			stableKey, stable := liveActivityCacheRun(t, v, 1)
			changedKey, before := liveActivityCacheRun(t, v, 3)
			switch change {
			case "paced reveal":
				if !v.testPace(now.Add(activityui.PaceStep)) {
					t.Fatal("due operation did not reveal")
				}
			case "settled output":
				if !settleActivity(now.Add(time.Minute), v) {
					t.Fatal("completed output did not collapse")
				}
			case "receipt":
				v.ApplyCapturedEdits([]*changepresentation.Receipt{{Thread: entry.Native.Thread, Calls: []string{entry.Native.Item}, Text: "Edit `result.go` +3 -1"}})
				v.publishActivity()
			default:
				update := entry
				update.Seq = v.LastSeq + 1
				native := *entry.Native
				update.Native = &native
				switch change {
				case "output delta":
					update.OutputTail = []string{"hello", "second output line"}
				case "reasoning":
					update.Text = "**Checking the updated result**\nUpdated public summary"
				case "exit":
					update.Kind, update.Text, update.Native = "exit", "1", nil
				}
				v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{update}})
			}
			if _, present := v.Runs[changedKey]; present {
				t.Fatal("in-place update retained its stale cached run")
			}
			retained, present := v.Runs[stableKey]
			if !present || &retained.Lines[0] != &stable.Lines[0] {
				t.Fatal("in-place update discarded unrelated completed run storage")
			}
			assertLiveActivityCacheFresh(t, v)
			afterKey, after := liveActivityCacheRun(t, v, 3)
			if change != "paced reveal" && change != "settled output" && afterKey.Revision <= changedKey.Revision {
				t.Fatal("entry update did not advance its presentation cache revision")
			}
			if reflect.DeepEqual(before.Lines, after.Lines) {
				t.Fatal("entry update did not change rendered output")
			}
			_, retained = liveActivityCacheRun(t, v, 1)
			if &retained.Lines[0] != &stable.Lines[0] {
				t.Fatal("render rebuilt the unrelated completed run")
			}
		})
	}
}

func TestLiveActivityCacheMainUpdateRemainsFresh(t *testing.T) {
	v := newActivityAdapter()
	v.Conversation = true
	v.testAudience()
	v.Painter.Theme = diffrender.DarkTheme
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.Clock = func() time.Time { return now }
	v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "You", Kind: "text", Text: "Explain the result", Observed: now}}})
	applyTranscriptFixture(v, true, "", "main", "main", "turn", "reply", "item/agentMessage/delta", "Initial explanation", binding.ThreadItem{})
	v.RenderFeed(90, 24)
	stableKey, stable := liveActivityCacheRun(t, v, 1)
	changedKey, _ := liveActivityCacheRun(t, v, 2)
	applyTranscriptFixture(v, true, "", "main", "main", "turn", "reply", "item/agentMessage/delta", " with a continuation", binding.ThreadItem{})
	if _, present := v.Runs[changedKey]; present {
		t.Fatal("Main update retained its stale run")
	}
	retained, present := v.Runs[stableKey]
	if !present || &retained.Lines[0] != &stable.Lines[0] {
		t.Fatal("Main stream update discarded completed prefix storage")
	}
	assertLiveActivityCacheFresh(t, v)
	if !strings.Contains(strings.Join(v.RenderFeed(90, 24).Lines, "\n"), "continuation") {
		t.Fatal("Main reply continuation missing")
	}
}

func TestLiveActivityCacheMainInvalidatesLaterQuotes(t *testing.T) {
	v := newActivityAdapter()
	v.Conversation = true
	v.testAudience()
	v.Painter.Theme = diffrender.DarkTheme
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.Clock = func() time.Time { return now }
	v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "Main", Kind: "text", Text: "Completed prefix", Observed: now},
		{Seq: 2, Agent: "You", Kind: "text", Text: "Original prompt", Observed: now, Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: "prompt"}},
		{Seq: 3, Agent: "/root/worker", Kind: "text", Text: "Intervening agent activity", Observed: now},
		{Seq: 4, Agent: "Main", Kind: "text", Text: "Answer to the prompt", Observed: now, Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: "answer"}},
	}})
	v.RenderFeed(90, 24)
	stableKey, stable := liveActivityCacheRun(t, v, 1)
	laterKey, later := liveActivityCacheRun(t, v, 4)
	if !strings.Contains(strings.Join(later.Lines, "\n"), "Original prompt") {
		t.Fatal("fixture did not quote the earlier prompt")
	}
	entry := v.Entries[1].Source
	entry.Text = "Updated prompt"
	v.replaceEntry(1, entry, parseLiveActivity(entry))
	v.publishActivity()
	if _, present := v.Runs[laterKey]; present {
		t.Fatal("prompt update kept a later dependent quote")
	}
	retained, present := v.Runs[stableKey]
	if !present || &retained.Lines[0] != &stable.Lines[0] {
		t.Fatal("prompt update discarded completed prefix storage")
	}
	assertLiveActivityCacheFresh(t, v)
	_, updated := liveActivityCacheRun(t, v, 4)
	if !strings.Contains(strings.Join(updated.Lines, "\n"), "Updated prompt") {
		t.Fatal("later reply retained the old quoted prompt")
	}
}

func BenchmarkLiveActivityCacheCommandDelta(b *testing.B) {
	for _, main := range []bool{false, true} {
		mode := "activity"
		if main {
			mode = "main"
		}
		b.Run(mode, func(b *testing.B) {
			for _, cold := range []bool{false, true} {
				name := "cached"
				if cold {
					name = "full-invalidation"
				}
				b.Run(name, func(b *testing.B) {
					now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
					v := newActivityAdapter()
					v.Conversation, v.ChildrenOnly = main, !main
					v.testAudience()
					v.Painter.Theme = diffrender.DarkTheme
					v.Clock = func() time.Time { return now }
					for i := range 80 {
						agent := fmt.Sprintf("/root/agent%d", i)
						if main {
							agent = "Main"
						}
						v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: agent, Kind: "text", Text: strings.Repeat("Completed analysis of the implementation.\n", 8), Observed: now}}})
					}
					agent := "/root/working"
					if main {
						agent = "Main"
					}
					entry := sessionpresentation.ActivityEntry{Seq: 81, Agent: agent, Kind: "tool", Text: "Run `go test ./...`", Observed: now,
						Native: &sessionpresentation.NativeItem{Thread: "child", Turn: "turn", Item: "command", Phase: "item/commandExecution/outputDelta", Running: true}, OutputTail: []string{"package result 0"}}
					v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
					v.RenderFeed(90, 24)
					b.ReportAllocs()
					iteration := 0
					for b.Loop() {
						entry.Seq = v.LastSeq + 1
						entry.OutputTail = []string{fmt.Sprintf("package result %d", iteration%10)}
						v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
						if cold {
							v.Runs = nil
						}
						v.RenderFeed(90, 24)
						iteration++
					}
				})
			}
		})
	}
}
