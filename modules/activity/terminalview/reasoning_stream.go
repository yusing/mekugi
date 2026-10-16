package terminalview

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
)

type reasoningRun struct {
	entry  observation.TextUpdate
	output OutputTail
	dirty  bool
	done   *observation.TextUpdate
}

// ReasoningStream owns terminal reveal, independently of host completion.
// Source: apps/mekugi/internal/router/app_server_summary_stream.go:11:111@[453772ac].
type ReasoningStream struct {
	runs map[observation.ItemRef]*reasoningRun
}

func (s *ReasoningStream) Queue(updates []observation.TextUpdate) []observation.TextUpdate {
	if s.runs == nil {
		s.runs = make(map[observation.ItemRef]*reasoningRun)
	}
	shown := updates[:0]
	for _, entry := range updates {
		if entry.Kind != "reasoning" || !entry.Live {
			shown = append(shown, entry)
			continue
		}
		key := entry.Ref
		if entry.Phase == "discarded" || strings.TrimSpace(entry.Text) == "" {
			delete(s.runs, key)
			shown = append(shown, entry)
			continue
		}
		run := s.runs[key]
		reserve := run == nil
		if run == nil {
			run = &reasoningRun{}
			s.runs[key] = run
		}
		if run.done != nil {
			continue
		}
		previous := run.entry.Text
		if !strings.HasPrefix(entry.Text, previous) {
			run.output = OutputTail{}
			previous = ""
		}
		run.output.Write(entry.Text[len(previous):])
		if entry.Phase == "item/completed" {
			done := entry
			run.done = &done
		}
		entry.Phase, entry.Thought = "summary", 0
		if entry.Replaces == "" {
			entry.Replaces = run.entry.Replaces
		}
		run.entry, run.dirty = entry, true
		if reserve {
			entry.Text, entry.Phase = "", "queued-summary"
			shown = append(shown, entry)
		}
	}
	return shown
}

func (s *ReasoningStream) Advance(at time.Time) []observation.TextUpdate {
	var updates []observation.TextUpdate
	for key, run := range s.runs {
		if rolled := run.output.Roll(); !run.dirty && !rolled {
			continue
		}
		run.dirty = false
		entry := run.entry
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
		updates = append(updates, entry)
		if run.done != nil && run.output.Pending() == 0 {
			done := *run.done
			done.Completed = at
			updates = append(updates, done)
			delete(s.runs, key)
		}
	}
	slices.SortStableFunc(updates, func(a, b observation.TextUpdate) int { return cmp.Compare(a.Sequence, b.Sequence) })
	return updates
}

func (s *ReasoningStream) FinishCompleted() {
	for _, run := range s.runs {
		if run.done != nil {
			run.output.Flush()
			run.dirty = true
		}
	}
}
