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

// CommandFrame requests reprojection when either source annotations or revealed
// output changed. The application supplies a detached projection of current source.
func (v *View) CommandFrame(seq, sourceRevision uint64) (CommandFrame, bool) {
	frame := v.commandFrames[seq]
	if frame == nil || !frame.released.IsZero() || !frame.project && frame.sourceRevision == sourceRevision {
		return CommandFrame{}, false
	}
	return frame.snapshot, true
}

func (v *View) UpdateCommandFrame(entry presentation.Entry) {
	frame := v.commandFrames[entry.Seq]
	if frame == nil {
		return
	}
	frame.entry, frame.sourceRevision = entry, entry.Revision
	frame.project, frame.dirty = false, true
}

func (v *View) ReleaseCommand(seq uint64, at time.Time) {
	if frame := v.commandFrames[seq]; frame != nil {
		frame.released = at
	}
}

func (v *View) commandEntry(entry presentation.Entry) presentation.Entry {
	if frame := v.commandFrames[entry.Seq]; frame != nil && frame.released.IsZero() && frame.entry.Seq == entry.Seq {
		shown := frame.entry
		shown.Outputs = entry.Outputs
		return shown
	}
	return entry
}
