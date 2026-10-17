package terminalsession

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Streams holds terminal pacing independently of source-operation correlation.
// Replacement creates fresh cursors; seeking settles only their reveal debt.
type Streams struct {
	Reasoning activityui.ReasoningStream
	Commands  map[[3]string]*CommandStream
}

func (s *Streams) Command(key [3]string, text string) *CommandStream {
	if s.Commands == nil {
		s.Commands = make(map[[3]string]*CommandStream)
	}
	if s.Commands[key] == nil {
		s.Commands[key] = &CommandStream{CommandStream: activityui.NewCommandStream(text)}
	}
	return s.Commands[key]
}

// CommandStream retains per-audience display identities after source correlation
// ends. The same output cursor can finish without keeping a command operation alive.
type CommandStream struct {
	*activityui.CommandStream
	MainSequence, ChildSequence uint64
}

func (s *Streams) DeliverCommand(d sessionpresentation.CommandDelivery, main, child *activityui.View) {
	stream := s.Commands[d.Key]
	switch d.Kind {
	case sessionpresentation.CommandBegin:
		stream = s.Command(d.Key, d.Text)
		stream.MainSequence, stream.ChildSequence = d.MainSequence, d.ChildSequence
	case sessionpresentation.CommandDelta:
		if stream != nil {
			stream.Output.Write(d.Delta)
		}
	case sessionpresentation.CommandComplete:
		if stream == nil {
			return
		}
		stream.MainSequence, stream.ChildSequence = d.MainSequence, d.ChildSequence
		if d.Tracked {
			frame := activityui.CommandFrame{Output: activityui.OutputFrame{Tail: d.Fallback.Tail, Omitted: d.Fallback.TailOmitted}, Segments: d.Fallback.Segments}
			if main != nil && stream.MainSequence != 0 {
				main.HoldCurrentCommand(stream.MainSequence, frame)
			}
			if child != nil && stream.ChildSequence != 0 {
				child.HoldCurrentCommand(stream.ChildSequence, frame)
			}
		} else if stream.Output.Pending() {
			stream.Holding = true
			stream.Hold(main, child, activityui.CommandFrame{Output: stream.Output.Snapshot()})
		} else {
			stream.Release(main, child, d.At)
			delete(s.Commands, d.Key)
		}
	}
}

func (s *CommandStream) Hold(main, child *activityui.View, frame activityui.CommandFrame) {
	if main != nil && s.MainSequence != 0 {
		main.HoldCommand(s.MainSequence, frame)
	}
	if child != nil && s.ChildSequence != 0 {
		child.HoldCommand(s.ChildSequence, frame)
	}
}

func (s *CommandStream) Release(main, child *activityui.View, at time.Time) {
	if main != nil && s.MainSequence != 0 {
		main.ReleaseCommand(s.MainSequence, at)
	}
	if child != nil && s.ChildSequence != 0 {
		child.ReleaseCommand(s.ChildSequence, at)
	}
}
