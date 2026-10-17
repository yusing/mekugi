package terminalview

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// A reasoning frame is terminal reveal over the already admitted source item.
// Completion remains available to every source consumer while this cursor drains.
type reasoningFrame struct {
	update         observation.TextUpdate
	entry          presentation.Entry
	sourceRevision uint64
	dirty          bool
}

func (v *View) HoldReasoning(update observation.TextUpdate) {
	if update.Kind != "reasoning" || !update.Live {
		return
	}
	if update.Phase == "discarded" || update.Phase != "queued-summary" && strings.TrimSpace(update.Text) == "" {
		delete(v.reasoningFrames, update.Ref)
		return
	}
	if v.reasoningFrames == nil {
		v.reasoningFrames = make(map[observation.ItemRef]*reasoningFrame)
	}
	frame := v.reasoningFrames[update.Ref]
	if frame == nil {
		frame = new(reasoningFrame)
		v.reasoningFrames[update.Ref] = frame
	}
	if frame.update.Kind != "" && presentation.ReasoningSummaryHeader(frame.update.Text) == presentation.ReasoningSummaryHeader(update.Text) {
		update.Observed = frame.update.Observed
	}
	if frame.update.Phase == "item/completed" {
		if update.Phase != "item/completed" {
			return
		}
		update.Thought, update.Observed, update.Completed = frame.update.Thought, frame.update.Observed, frame.update.Completed
	}
	frame.update, frame.dirty = update, true
}

func (v *View) reasoningEntry(entry presentation.Entry) presentation.Entry {
	frame := v.reasoningFrames[entry.TextRef]
	if frame == nil {
		return entry
	}
	if frame.dirty || frame.sourceRevision != entry.Revision {
		shown := entry
		shown.Text, shown.Observed, shown.Completed = frame.update.Text, frame.update.Observed, frame.update.Completed
		shown.Suppressed = frame.update.Phase == "queued-summary"
		shown.ItemComplete = frame.update.Phase == "item/completed"
		if !shown.ItemComplete {
			shown.Completed = time.Time{}
		}
		content := presentation.ItemContent{Facts: shown.Facts, Details: &presentation.ItemDetails{
			ReasoningLive: frame.update.Phase == "summary" || frame.update.Phase == "item/started", Thought: frame.update.Thought,
		}}
		shown.Values = content.Values()
		frame.entry, frame.sourceRevision = shown, entry.Revision
	}
	return frame.entry
}
