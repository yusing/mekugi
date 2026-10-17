package router

import (
	"fmt"
	"slices"
	"testing"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestLiveActivityRecordRetentionAndHistoryInsertion(t *testing.T) {
	v := newActivityAdapter()
	v.ChildrenOnly = true
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.Clock = func() time.Time { return now }
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
	v.RenderFeed(90, 24)
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
	v := newActivityAdapter()
	v.ChildrenOnly = true
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	v.Clock = func() time.Time { return now }
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/finished", Kind: "text", Text: "Independent result", Observed: now},
		{Seq: 2, Agent: "/root/other", Kind: "text", Text: "Another result", Observed: now},
		{Seq: 3, Agent: "/root/working", Kind: "text", Text: "Original result", Observed: now},
	}})
	v.RenderFeed(90, 24)
	stableKey, stable := liveActivityCacheRun(t, v, 1)
	changedKey, _ := liveActivityCacheRun(t, v, 3)
	revision := v.Entries[2].Revision
	v.Model.Mutate(2, func(record *liveActivityRecord) {
		record.Source.Observed = now.Add(time.Hour)
		record.Source.Text = "Updated result"
		record.Values = activityui.Values(parseLiveActivity(record.Source))
	})
	if v.Entries[2].Revision <= revision {
		t.Fatal("source and annotation mutation did not advance its revision")
	}
	v.publishActivity()
	v.publishActivity() // Repeated delivery cannot invalidate an unchanged run.
	if _, present := v.Runs[changedKey]; present {
		t.Fatal("source and annotation mutation retained its cached run")
	}
	retained, present := v.Runs[stableKey]
	if !present || &retained.Lines[0] != &stable.Lines[0] {
		t.Fatal("record mutation discarded an unrelated completed run")
	}
	assertLiveActivityCacheFresh(t, v)
}

func (v *activityAdapter) blocks(index int) []activityui.Block {
	v.publishActivity()
	return v.Disclosure.Blocks(v.Entries[index].Values)
}

// Renderer fixtures supply blocks; source production retains only neutral values.
func (v *activityAdapter) appendEntry(entry activityPaneEntry, blocks []activityui.Block) {
	v.Model.Append(entry, activityui.Values(blocks))
}
func (v *activityAdapter) replaceEntry(index int, entry activityPaneEntry, blocks []activityui.Block) {
	v.Model.Replace(index, entry, activityui.Values(blocks))
}
func parseLiveActivity(entry activityPaneEntry) []activityui.Block {
	return activityui.Disclosure(nil).Blocks(activityValues(entry))
}
func commandSegmentBlocks(command activitypresentation.Command) []activityui.Block {
	return activityui.Disclosure(nil).Blocks(activitypresentation.GroupOperations(command.SegmentValues(false)))
}
func commandExitBlocks(blocks []activityui.Block, code int, tail []string, omitted int) []activityui.Block {
	return activityui.Disclosure(nil).Blocks(activitypresentation.CommandExit(activityui.Values(blocks), code, tail, omitted))
}
