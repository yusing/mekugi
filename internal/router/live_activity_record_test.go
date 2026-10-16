package router

import (
	"fmt"
	"slices"
	"testing"
	"time"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestLiveActivityRecordRetentionAndHistoryInsertion(t *testing.T) {
	v := newLiveActivityView()
	v.childrenOnly = true
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.clock = func() time.Time { return now }
	entries := make([]activityPaneEntry, liveActivityFeedLimit)
	for i := range entries {
		entries[i] = activityPaneEntry{Seq: uint64(i + 1), Agent: "/root/worker", Kind: "text", Text: fmt.Sprint("Retained item ", i), Observed: now}
	}
	entries[2] = activityPaneEntry{Seq: 3, Agent: "/root/worker", Kind: "tool", Text: "Run `printf result`", Observed: now,
		native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "command", phase: "item/completed"}, outputTail: []string{"result"}}
	v.apply(activityPaneEvent{Kind: "entries", Entries: entries})
	if !v.markUnreturned("child", "command") {
		t.Fatal("output annotation missing")
	}
	revision := v.Entries[2].Revision
	v.renderFeed(90, 24)
	before := v.LastSeq
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: before + 1, Agent: "/root/worker", Kind: "text", Text: "Older page one", Observed: now},
		{Seq: before + 2, Agent: "/root/worker", Kind: "text", Text: "Older page two", Observed: now},
	}})
	if len(v.Entries) != liveActivityFeedLimit || v.Entries[0].Source.Seq != 3 {
		t.Fatal("retention did not discard only the two oldest records")
	}
	v.insertHistory(before, func(entry activityPaneEntry) bool { return entry.Seq == 3 })
	i := slices.IndexFunc(v.Entries, func(record liveActivityRecord) bool { return record.Source.Seq == 3 })
	if i != 2 || v.Entries[i].Revision != revision || v.Entries[i].Source.native.item != "command" {
		t.Fatal("history insertion detached retained presentation identity")
	}
	blocks := v.blocks(i)
	if len(blocks) != 2 || blocks[0].Label != "`printf result`" || !slices.Equal(blocks[0].Tail, []string{"result"}) || blocks[1].Body != unreturnedOutputNote {
		t.Fatalf("retention or reordering lost source output or its annotation: %+v", blocks)
	}
	assertLiveActivityCacheFresh(t, v)
}

func TestLiveActivityRecordMutationOwnsInvalidation(t *testing.T) {
	v := newLiveActivityView()
	v.childrenOnly = true
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.clock = func() time.Time { return now }
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/finished", Kind: "text", Text: "Independent result", Observed: now},
		{Seq: 2, Agent: "/root/other", Kind: "text", Text: "Another result", Observed: now},
		{Seq: 3, Agent: "/root/working", Kind: "text", Text: "Original result", Observed: now},
	}})
	v.renderFeed(90, 24)
	stableKey, stable := liveActivityCacheRun(t, v, 1)
	changedKey, _ := liveActivityCacheRun(t, v, 3)
	revision := v.Entries[2].Revision
	v.mutateEntry(2, func(record *liveActivityRecord) {
		record.Source.Observed = now.Add(time.Hour)
		record.Source.Text = "Updated result"
		record.Values = activityui.Values(parseLiveActivity(record.Source))
	})
	if v.Entries[2].Revision <= revision {
		t.Fatal("source and annotation mutation did not advance its revision")
	}
	if _, present := v.runs[changedKey]; present {
		t.Fatal("source and annotation mutation retained its cached run")
	}
	retained, present := v.runs[stableKey]
	if !present || &retained.lines[0] != &stable.lines[0] {
		t.Fatal("record mutation discarded an unrelated completed run")
	}
	assertLiveActivityCacheFresh(t, v)
}
