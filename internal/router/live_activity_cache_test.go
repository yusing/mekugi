package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

// Compare the complete feed and hit targets, not just the visible text: an
// unchanged rendering must not retain stale snippets or question navigation.
func assertLiveActivityCacheFresh(t *testing.T, v *liveActivityView) {
	t.Helper()
	got := v.renderFeed(90, 24)
	rows := v.questionRows
	fresh := *v
	fresh.runs = nil
	want := fresh.renderFeed(90, 24)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(rows, fresh.questionRows) {
		t.Fatalf("cached feed differs from fresh render:\ngot: %#v\nwant: %#v\nrows: %v / %v", got, want, rows, fresh.questionRows)
	}
	for key, run := range v.runs {
		if !reflect.DeepEqual(run, fresh.runs[key]) {
			t.Fatalf("cached run %v has stale lines, snippets, questions, blocks or entryRows", key)
		}
	}
}

func liveActivityCacheRun(t *testing.T, v *liveActivityView, seq uint64) (liveActivityRunKey, liveActivityRun) {
	t.Helper()
	for key, run := range v.runs {
		if key.first <= seq && seq <= key.last {
			return key, run
		}
	}
	t.Fatalf("no cached run containing entry %d", seq)
	return liveActivityRunKey{}, liveActivityRun{}
}

func TestLiveActivityCacheEntryUpdates(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, change := range []string{"output delta", "reasoning", "exit", "receipt", "paced reveal", "settled output"} {
		t.Run(change, func(t *testing.T) {
			v := newLiveActivityView()
			v.childrenOnly = true
			v.painter.Theme = livediff.DarkTheme
			v.clock = func() time.Time { return now }
			entry := activityPaneEntry{Seq: 3, Agent: "/root/working", Kind: "tool", CallID: "command", Text: "Run `printf hello`", Observed: now,
				native:     &liveActivityNativeItem{thread: "child", turn: "turn", item: "command", phase: "item/commandExecution/outputDelta"},
				outputTail: []string{"hello"}}
			switch change {
			case "reasoning":
				entry.Kind, entry.Text, entry.CallID = "reasoning", "**Checking the result**\nInitial public summary", "summary"
			case "paced reveal":
				entry.Text = "List `.`\n\nSearch `needle`\n\nRead `a.go`"
				entry.native.live = true
			case "settled output":
				entry.native.phase, entry.native.settled = "item/completed", now
				// A one-line result is already compact and is not collapsible.
				entry.outputTail = []string{"hello", "completed second output line"}
			}
			entries := []activityPaneEntry{
				{Seq: 1, Agent: "/root/finished", Kind: "text", Text: "Completed independent agent work", Observed: now},
				{Seq: 2, Agent: "/root/other", Kind: "text", Text: "Another independent result", Observed: now}, entry,
			}
			if change == "settled output" {
				entries = append(entries, activityPaneEntry{Seq: 4, Agent: entry.Agent, Kind: "text", Text: "The next event settles output", Observed: now})
			}
			v.apply(activityPaneEvent{Kind: "entries", Entries: entries})
			v.renderFeed(90, 24)
			stableKey, stable := liveActivityCacheRun(t, v, 1)
			changedKey, before := liveActivityCacheRun(t, v, 3)
			switch change {
			case "paced reveal":
				if !v.pace(now.Add(liveActivityPaceStep)) {
					t.Fatal("due operation did not reveal")
				}
			case "settled output":
				if !settleActivity(now.Add(time.Minute), v) {
					t.Fatal("completed output did not collapse")
				}
			default:
				update := entry
				update.Seq = v.lastSeq + 1
				native := *entry.native
				update.native = &native
				switch change {
				case "output delta":
					update.outputTail = []string{"hello", "second output line"}
				case "reasoning":
					update.Text = "**Checking the result**\nUpdated public summary"
				case "exit":
					update.Kind, update.Text, update.native = "exit", "1", nil
				case "receipt":
					update.Text, update.native = "Edit `result.go` +3 -1", nil
				}
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{update}})
			}
			if _, present := v.runs[changedKey]; present {
				t.Fatal("in-place update retained its stale cached run")
			}
			retained, present := v.runs[stableKey]
			if !present || &retained.lines[0] != &stable.lines[0] {
				t.Fatal("in-place update discarded unrelated completed run storage")
			}
			assertLiveActivityCacheFresh(t, v)
			_, after := liveActivityCacheRun(t, v, 3)
			if reflect.DeepEqual(before.lines, after.lines) {
				t.Fatal("entry update did not change rendered output")
			}
			_, retained = liveActivityCacheRun(t, v, 1)
			if &retained.lines[0] != &stable.lines[0] {
				t.Fatal("render rebuilt the unrelated completed run")
			}
		})
	}
}

func TestLiveActivityCacheMainUpdateRemainsFresh(t *testing.T) {
	v := newLiveActivityView()
	v.conversation = true
	v.painter.Theme = livediff.DarkTheme
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.clock = func() time.Time { return now }
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "You", Kind: "text", Text: "Explain the result", Observed: now}}})
	v.applyAppServerItem("", "main", "main", "turn", "reply", "item/agentMessage/delta", "Initial explanation", appServerItem{})
	v.renderFeed(90, 24)
	if len(v.runs) == 0 {
		t.Fatal("Main fixture did not warm the run cache")
	}
	v.applyAppServerItem("", "main", "main", "turn", "reply", "item/agentMessage/delta", " with a continuation", appServerItem{})
	if len(v.runs) != 0 {
		t.Fatal("Main update kept cross-entry dependent cached runs")
	}
	assertLiveActivityCacheFresh(t, v)
	if !strings.Contains(strings.Join(v.renderFeed(90, 24).lines, "\n"), "continuation") {
		t.Fatal("Main reply continuation missing")
	}
}

func BenchmarkLiveActivityCacheCommandDelta(b *testing.B) {
	for _, cold := range []bool{false, true} {
		name := "cached"
		if cold {
			name = "full-invalidation"
		}
		b.Run(name, func(b *testing.B) {
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			v := newLiveActivityView()
			v.childrenOnly = true
			v.painter.Theme = livediff.DarkTheme
			v.clock = func() time.Time { return now }
			for i := range 80 {
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: uint64(i + 1), Agent: fmt.Sprintf("/root/agent%d", i), Kind: "text", Text: strings.Repeat("Completed analysis of the implementation.\n", 8), Observed: now}}})
			}
			entry := activityPaneEntry{Seq: 81, Agent: "/root/working", Kind: "tool", Text: "Run `go test ./...`", Observed: now,
				native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "command", phase: "item/commandExecution/outputDelta", running: true}, outputTail: []string{"package result 0"}}
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
			v.renderFeed(90, 24)
			b.ReportAllocs()
			iteration := 0
			for b.Loop() {
				entry.Seq = v.lastSeq + 1
				entry.outputTail = []string{fmt.Sprintf("package result %d", iteration%10)}
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
				if cold {
					v.runs = nil
				}
				v.renderFeed(90, 24)
				iteration++
			}
		})
	}
}
