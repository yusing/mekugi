package terminalview

// Source: apps/mekugi/internal/router/live_activity_events.go:11:64@[29de53cc].

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/terminalui"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// sharedEvent keeps event framing in the transcript renderer. Operations and
// reasoning retain Activity's invocation grouping and branch navigation.
func (v *View) sharedEvent(index int) bool {
	return slices.Contains([]string{"text", "reply", "assignment", "start", "final", "error", "compaction"}, v.entries[index].Kind)
}

// activityEventItem derives event presentation from Main, but retains Activity's
// bounded narrative preview and local detail target rather than Main's linked
// excerpts and conversation threading.
func (v *View) activityEventItem(index, width, clip int, flash uint64) Run {
	entry := v.entries[index]
	run := v.ConversationItem(index, index, width, ConversationThread{})
	if run.EntryRows == nil {
		run.EntryRows = make(map[uint64]int)
	}
	run.EntryRows[entry.Seq] = 0
	if flash == entry.Seq && entry.Kind != "final" && len(run.Lines) > 1 {
		for i := 1; i < len(run.Lines); i++ {
			rail, text, found := strings.Cut(run.Lines[i], " ")
			if found {
				run.Lines[i] = rail + " " + v.Painter.Flash(Block{Flash: true}, []string{text})[0]
			}
		}
	}
	if clip > 0 && len(run.Lines) > clip+1 {
		run.Blocks = v.blocks(index)
	}
	v.clipEvent(&run, entry, clip)
	// Retain the source identity for dialog refresh while text streams or settles.
	run.Blocks = slices.Clone(run.Blocks)
	for i := range run.Blocks {
		run.Blocks[i].Source = entry.Seq
	}
	return run
}

func (v *View) clipEvent(run *Run, entry activitypresentation.Entry, clip int) {
	if clip > 0 && len(run.Lines) > clip+1 {
		snippet := Snippet{Run: entry.Seq, Block: 0}
		gutter := Gutter(entry.Agent, v.Painter.Theme) + "│" + terminalui.Reset + " "
		if v.Conversation && entry.Agent == "Main" && entry.Kind == "text" {
			gutter = mainGutter(&v.Painter)
		}
		// Keep the heading outside the body budget. The dialog retains the full entry.
		run.Lines = append(run.Lines[:clip:clip], gutter+
			Elision{Hidden: len(run.Lines) - clip, Hovered: v.Snippet == snippet}.String())
		run.Snippets = make([]Snippet, len(run.Lines))
		run.Questions = run.Questions[:len(run.Lines)]
		for i := 1; i < len(run.Snippets); i++ {
			run.Snippets[i] = snippet
		}
	}
}
