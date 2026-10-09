package router

import (
	"slices"
	"strings"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// sharedEvent keeps event framing in the transcript renderer. Operations and
// reasoning retain Activity's invocation grouping and branch navigation.
func (v *liveActivityView) sharedEvent(index int) bool {
	if blocks := v.entries[index].blocks; len(blocks) == 1 && blocks[0].Kind == "journal" && v.entries[index].Agent != "Main" {
		return false // Child journal updates stay in their agent's operation group.
	}
	return slices.Contains([]string{"text", "reply", "assignment", "start", "final", "error", "compaction"}, v.entries[index].Kind)
}

// activityEventItem derives event presentation from Main, but retains Activity's
// bounded narrative preview and local detail target rather than Main's linked
// excerpts and conversation threading.
func (v *liveActivityView) activityEventItem(index, width, clip int, flash uint64) liveActivityRun {
	entry := v.entries[index]
	run := v.conversationItem(index, index, width, conversationThread{})
	if run.entryRows == nil {
		run.entryRows = make(map[uint64]int)
	}
	run.entryRows[entry.Seq] = 0
	if flash == entry.Seq && entry.Kind != "final" && len(run.lines) > 1 {
		for i := 1; i < len(run.lines); i++ {
			rail, text, found := strings.Cut(run.lines[i], " ")
			if found {
				run.lines[i] = rail + " " + v.painter.Flash(activityui.Block{Flash: true}, []string{text})[0]
			}
		}
	}
	if clip > 0 && len(run.lines) > clip+1 {
		run.blocks = entry.blocks
	}
	v.clipEvent(&run, entry.activityPaneEntry, clip)
	// Retain the source identity for dialog refresh while text streams or settles.
	run.blocks = slices.Clone(run.blocks)
	for i := range run.blocks {
		run.blocks[i].Source = entry.Seq
	}
	return run
}

func (v *liveActivityView) clipEvent(run *liveActivityRun, entry activityPaneEntry, clip int) {
	if clip > 0 && len(run.lines) > clip+1 {
		snippet := liveActivitySnippet{run: entry.Seq, block: 0}
		gutter := activityui.Gutter(entry.Agent, v.painter.Theme) + "│" + activityui.Reset + " "
		if v.conversation && entry.Agent == "Main" && entry.Kind == "text" {
			gutter = mainGutter(&v.painter)
		}
		// Keep the heading outside the body budget. The dialog retains the full entry.
		run.lines = append(run.lines[:clip:clip], gutter+
			activityui.Elision{Hidden: len(run.lines) - clip, Hovered: v.snippet == snippet}.String())
		run.snippets = make([]liveActivitySnippet, len(run.lines))
		run.questions = run.questions[:len(run.lines)]
		for i := 1; i < len(run.snippets); i++ {
			run.snippets[i] = snippet
		}
	}
}
