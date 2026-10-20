package terminalsession

import (
	"time"

	"github.com/yusing/mekugi/contracts/observation"

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

// Deliver applies detached source transitions to the terminal's pacing cursors.
// Source: apps/mekugi/internal/router/activity_command_frame.go:19:33@4b40d245.
func (s *Streams) Deliver(deliveries []sessionpresentation.ActivityDelivery, main, child *activityui.View) {
	for _, delivery := range deliveries {
		switch {
		case delivery.Text != nil:
			s.QueueText([]observation.TextUpdate{*delivery.Text}, main, child)
		case delivery.Command != nil:
			s.DeliverCommand(*delivery.Command, main, child)
		case delivery.Rename != nil:
			RenameSelection(delivery.Rename.Old, delivery.Rename.Name, main, child)
		}
	}
}

func (s *Streams) QueueText(updates []observation.TextUpdate, views ...*activityui.View) {
	for _, update := range s.Reasoning.Queue(updates) {
		for _, view := range views {
			if view != nil {
				view.HoldReasoning(update)
			}
		}
	}
}

func (s *Streams) AdvanceReasoning(at time.Time, views ...*activityui.View) bool {
	updates := s.Reasoning.Advance(at)
	for _, update := range updates {
		for _, view := range views {
			if view != nil {
				view.HoldReasoning(update)
			}
		}
	}
	return len(updates) != 0
}

// AdvanceCommands reveals output not already paced by live segment integration.
// Source: apps/mekugi/internal/router/app_server_events.go:155:170@4b40d245.
func (s *Streams) AdvanceCommands(at time.Time, handled map[[3]string]bool, main, child *activityui.View) (changed bool) {
	for key, stream := range s.Commands {
		if handled[key] {
			continue
		}
		frame, advanced := stream.Output.Advance()
		if !advanced {
			continue
		}
		stream.Hold(main, child, activityui.CommandFrame{Output: frame})
		if stream.Holding && !frame.Pending {
			stream.Release(main, child, at)
			delete(s.Commands, key)
		}
		changed = true
	}
	return
}

// RenameSelection preserves terminal navigation across admitted caller metadata.
// Source: apps/mekugi/internal/router/app_server_thread_metadata.go:46:62@4b40d245.
func RenameSelection(old, name string, views ...*activityui.View) {
	if old == name {
		return
	}
	for _, view := range views {
		if view == nil {
			continue
		}
		if view.Selected == old {
			view.Selected = name
		}
		if view.RosterSelected == old {
			view.RosterSelected = name
		}
		view.Runs = nil
	}
}
