package contextnotice

import (
	"fmt"
	"math/bits"
	"sync"
)

type Sample struct{ Tokens, Window, Compactions uint64 }
type Notice struct{ Text, Reminder string }
type Mark struct {
	Index     int
	Key, Text string
}
type state struct {
	compactions, level uint64
	last               string
	marks              []Mark
}

// Store retains at most five small annotations per native session/thread.
type Store struct {
	mu      sync.Mutex
	threads map[[2]string]state
}

// Project acquires a fresh boundary only when a measured milestone is due.
func (s *Store) Project(session, thread string, sample Sample, target func(string) Mark) (Notice, []Mark) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = make(map[[2]string]state)
	}
	current := s.threads[[2]string{session, thread}]
	if sample.Window > 0 && current.compactions != sample.Compactions {
		current = state{compactions: sample.Compactions}
	}
	var notice Notice
	percent := uint64(0)
	if sample.Window > 0 {
		hi, lo := bits.Mul64(min(sample.Tokens, sample.Window), 100)
		percent, _ = bits.Div64(hi, lo, sample.Window)
	}
	if level := min(percent/10*10, 90); level >= 50 && level > current.level {
		if mark := target(current.last); mark.Index >= 0 {
			current.level, current.last = level, mark.Key
			notice.Text = fmt.Sprintf("Mekugi context usage: %d%% (%d/%d tokens).", percent, sample.Tokens, sample.Window)
			notice.Reminder = "<system-reminder>\n" + notice.Text + "\n</system-reminder>"
			mark.Text = notice.Reminder
			current.marks = append(current.marks, mark)
		}
	}
	s.threads[[2]string{session, thread}] = current
	return notice, append([]Mark(nil), current.marks...)
}
