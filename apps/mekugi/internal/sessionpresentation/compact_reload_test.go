package sessionpresentation

import (
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestReloadBundleMatchesBeforeOrAfterCommand(t *testing.T) {
	for _, late := range []bool{false, true} {
		var attached Attachment
		source := &ActivitySource{Model: &attached.Activity().Main}
		apply := func(entry ActivityEntry) {
			entry.Seq = source.LastSeq + 1
			source.Apply(ActivityEvent{Entries: []ActivityEntry{entry}})
		}
		intent := ActivityEntry{Kind: "compact_reload", Native: &NativeItem{Thread: "thread", Turn: "turn"}, ReloadSources: []activity.ReloadSource{
			{Command: "mreload selection", Path: "first.txt"}, {Command: "mreload selection", Path: "second.txt"},
		}}
		if !late {
			apply(intent)
		}
		apply(ActivityEntry{Kind: "tool", Native: &NativeItem{Thread: "thread", Turn: "turn", Item: "capture", Command: "mreload selection", Phase: "item/completed", Exit: new(0)}})
		if late {
			apply(intent)
		}
		reload := source.Entries[0].Source.Native.Reload
		if len(reload) != 2 || reload[0].Path != "first.txt" || reload[1].Path != "second.txt" {
			t.Fatalf("late=%v: bundle lost source identity: %+v", late, reload)
		}
		if len(source.reloads) != 0 {
			t.Fatalf("late=%v: completed plan retained", late)
		}
	}
}

func TestReloadPlansRetireAndPreserveLateMetadata(t *testing.T) {
	for _, reset := range []string{"compaction", "empty-selection", "completed"} {
		t.Run(reset, func(t *testing.T) {
			var attached Attachment
			source := &ActivitySource{Model: &attached.Activity().Main}
			seq := uint64(0)
			apply := func(entry ActivityEntry) {
				seq++
				entry.Seq = seq
				source.Apply(ActivityEvent{Entries: []ActivityEntry{entry}})
			}
			command := func(id, script string) ActivityEntry {
				return ActivityEntry{Kind: "tool", Text: "Read `source.txt`", Native: &NativeItem{Thread: "thread", Turn: "turn", Item: id, Command: script, CommandCwd: "/workspace", Phase: "item/completed", Status: "completed", Exit: new(0)}}
			}
			intent := ActivityEntry{Kind: "compact_reload", Native: &NativeItem{Thread: "thread"}, ReloadSources: []activity.ReloadSource{{Command: "mreload first-selection", Path: "source.txt"}}}
			// Metadata can arrive after the native row or after history restoration.
			apply(command("first", "mreload first-selection"))
			apply(intent)
			if source.Entries[0].Source.Native.Reload == nil {
				t.Fatal("late metadata lost")
			}
			apply(ActivityEntry{Kind: "compact_reload", Native: &NativeItem{Thread: "thread"}, ReloadSources: []activity.ReloadSource{{Command: "mreload next-selection", Path: "next.txt"}}})
			switch reset {
			case "compaction":
				apply(ActivityEntry{Kind: "compaction", Text: "retained summary", Native: &NativeItem{Thread: "thread", Item: "boundary"}})
			case "empty-selection":
				apply(ActivityEntry{Kind: "compact_reload", Native: &NativeItem{Thread: "thread"}})
			case "completed":
				apply(command("second", "mreload next-selection"))
			}
			if len(source.reloads) != 0 {
				t.Fatal("settled or superseded plan retained")
			}
			apply(command("ordinary", "mreload next-selection"))
			if source.Entries[len(source.Entries)-1].Source.Native.Reload != nil {
				t.Fatal("ordinary read mislabeled as auto reload")
			}
			if source.Entries[0].Source.Native.Reload == nil {
				t.Fatal("retiring plan discarded confirmed annotation")
			}
		})
	}
}
