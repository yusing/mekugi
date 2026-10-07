package router

import (
	"cmp"
	"slices"
	"strings"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// appServerSummaryRun uses the output stream's bounded rolling cursor, while
// retaining original Markdown rather than its sanitized terminal tail.
type appServerSummaryRun struct {
	entry  activityPaneEntry
	output activityui.OutputTail
	dirty  bool
	done   *activityPaneEntry
}

func (u *appServerUI) queueSummaries(entries []activityPaneEntry) []activityPaneEntry {
	shown := entries[:0]
	for _, entry := range entries {
		if entry.Kind != "reasoning" || entry.native == nil || !entry.native.live {
			shown = append(shown, entry)
			continue
		}
		key := [3]string{entry.native.thread, entry.native.turn, entry.native.item}
		if entry.native.phase == "discarded" || strings.TrimSpace(entry.Text) == "" {
			delete(u.session.summaries, key)
			shown = append(shown, entry)
			continue
		}
		run := u.session.summaries[key]
		reserve := run == nil
		if run == nil {
			run = &appServerSummaryRun{}
			u.session.summaries[key] = run
		}
		if run.done != nil {
			continue
		}
		previous := run.entry.Text
		if !strings.HasPrefix(entry.Text, previous) {
			run.output = activityui.OutputTail{}
			previous = ""
		}
		run.output.Write(entry.Text[len(previous):])
		if entry.native.phase == "item/completed" {
			done := entry
			run.done = &done
		}
		native := *entry.native
		native.phase, native.thought = "summary", 0
		// The first queued delta may have taken over a provider's waiting row.
		if native.replaces == "" && run.entry.native != nil {
			native.replaces = run.entry.native.replaces
		}
		entry.native = &native
		run.entry, run.dirty = entry, true
		if reserve {
			// Reserve chronology when public text arrives, without displaying
			// an empty Thinking row while it waits for its first paint tick.
			queued := entry
			queued.Text = ""
			queuedNative := native
			queuedNative.phase = "queued-summary"
			queued.native = &queuedNative
			shown = append(shown, queued)
		}
	}
	return shown
}

func (u *appServerUI) flushSummaryOutput() {
	var entries []activityPaneEntry
	for key, run := range u.session.summaries {
		if rolled := run.output.Roll(); !run.dirty && !rolled {
			continue
		}
		run.dirty = false
		entry := run.entry
		entry.Agent = u.session.path(key[0])
		end := 0
		for range run.output.VisibleThrough() {
			next := strings.IndexByte(entry.Text[end:], '\n')
			if next < 0 {
				end = len(entry.Text)
				break
			}
			end += next + 1
		}
		entry.Text = entry.Text[:end]
		entries = append(entries, entry)
		if run.done != nil && run.output.Pending() == 0 {
			done := *run.done
			done.Agent = entry.Agent
			entries = append(entries, done)
			delete(u.session.summaries, key)
		}
	}
	if len(entries) == 0 {
		return
	}
	slices.SortStableFunc(entries, func(a, b activityPaneEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for i := range entries {
		entries[i].Seq = u.session.next()
	}
	u.applyActivity(entries, nil)
	u.dirty = true
}
