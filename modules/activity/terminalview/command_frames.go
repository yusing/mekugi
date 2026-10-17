package terminalview

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/modules/activity/presentation"
)

// CommandFrame is the detached output and segment snapshot being displayed while
// terminal animation or report attribution catches up with admitted host facts.
type CommandFrame struct {
	Output   OutputFrame
	Segments []presentation.CommandSegment
}

type heldCommandFrame struct {
	snapshot       CommandFrame
	entry          presentation.Entry
	sourceRevision uint64
	revision       uint64
	project, dirty bool
	released       time.Time
}

func (v *View) HoldCommand(seq uint64, snapshot CommandFrame) {
	if v.commandFrames == nil {
		v.commandFrames = make(map[uint64]*heldCommandFrame)
	}
	frame := v.commandFrames[seq]
	if frame == nil {
		frame = new(heldCommandFrame)
		v.commandFrames[seq] = frame
	}
	snapshot.Output.Tail = slices.Clone(snapshot.Output.Tail)
	frame.snapshot, frame.project = snapshot, true
	frame.revision++
}

// CurrentCommandFrame supplies bounded terminal fallback content when the
// optional retention owner is absent. Its revision is independent of source facts.
func (v *View) CurrentCommandFrame(seq uint64) (CommandFrame, uint64, bool) {
	if frame := v.commandFrames[seq]; frame != nil && frame.released.IsZero() {
		return frame.snapshot, frame.revision, true
	}
	return CommandFrame{}, 0, false
}

// HoldCurrentCommand preserves the already displayed frame during report
// settlement. A command with no reveal frame uses its admitted source snapshot.
func (v *View) HoldCurrentCommand(seq uint64, fallback CommandFrame) {
	if frame := v.commandFrames[seq]; frame != nil {
		frame.project = true
		return
	}
	v.HoldCommand(seq, fallback)
}

// NeedsCommandContent requests detached source only for a held reveal frame
// whose source annotations or revealed output changed.
func (v *View) NeedsCommandContent(seq, revision uint64) bool {
	frame := v.commandFrames[seq]
	return frame != nil && frame.released.IsZero() && (frame.project || frame.sourceRevision != revision)
}

// Source: apps/mekugi/internal/router/activity_command_frame.go:14:26@e596fd5f.
// Project reuses admitted command content while delaying only terminal outcome
// and output reveal. It never resolves the original source or retained output.
func (frame CommandFrame) Project(entry presentation.Entry) presentation.Entry {
	if entry.Command == nil {
		return entry
	}
	content := *entry.Command
	content.Command.Running, content.Command.Exited, content.Command.ExitCode = true, false, 0
	content.Command.Ended, content.Command.Duration = time.Time{}, 0
	content.Command.Tail, content.Command.TailOmitted = frame.Output.Tail, frame.Output.Omitted
	content.Command.Segments = frame.Segments
	if content.Details != nil {
		content.Details = new(*content.Details)
		content.Details.Status = "inProgress"
	}
	entry.Values = presentation.RetainAnnotations(content.Values(), entry.Values)
	entry.Running, entry.ItemComplete, entry.Unconfirmed, entry.OutputComplete = true, false, false, false
	entry.Completed = time.Time{}
	return entry
}

func (v *View) ReleaseCommand(seq uint64, at time.Time) {
	if frame := v.commandFrames[seq]; frame != nil {
		frame.released = at
	}
}

func (v *View) updateCommandFrame(entry presentation.Entry) {
	if !v.NeedsCommandContent(entry.Seq, entry.Revision) || entry.Command == nil {
		return
	}
	frame := v.commandFrames[entry.Seq]
	frame.entry = frame.snapshot.Project(entry)
	frame.sourceRevision = entry.Revision
	frame.project, frame.dirty = false, true
}

func (v *View) commandEntry(entry presentation.Entry) presentation.Entry {
	if frame := v.commandFrames[entry.Seq]; frame != nil && frame.released.IsZero() && frame.entry.Seq == entry.Seq {
		shown := frame.entry
		shown.Outputs = entry.Outputs
		return shown
	}
	return entry
}
