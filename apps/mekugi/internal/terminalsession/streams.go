package terminalsession

import activityui "github.com/yusing/mekugi/modules/activity/terminalview"

// Streams holds terminal pacing independently of source-operation correlation.
// Replacement creates fresh cursors; seeking settles only their reveal debt.
type Streams struct {
	Reasoning activityui.ReasoningStream
	Commands  map[[3]string]*activityui.CommandStream
}

func (s *Streams) Command(key [3]string, text string) *activityui.CommandStream {
	if s.Commands == nil {
		s.Commands = make(map[[3]string]*activityui.CommandStream)
	}
	if s.Commands[key] == nil {
		s.Commands[key] = activityui.NewCommandStream(text)
	}
	return s.Commands[key]
}
