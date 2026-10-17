package presentation

import (
	"time"

	"github.com/yusing/mekugi/modules/activity/feed"
)

// SegmentObservation contains admitted source and helper-observed boundaries.
// Output is a newly drained byte chunk, never a replacement aggregate.
type SegmentObservation struct {
	Source, Text, Analysis string
	Started, Ended         time.Time
	Duration               time.Duration
	Began, Finished        bool
	ExitCode               int
	Output                 string
}

// SegmentReport has no transport capability or host identity. The existing
// invocation supplies its scope; application admission supplies ordered facts.
type SegmentReport struct {
	Segments                             []SegmentObservation
	Terminal, Lossy, Ended, Closed, Done bool
	ExitCode                             int
}

type SegmentSnapshot struct {
	CommandSegment
	Raw string // Bounded original bytes for admitted VCS interpretation.
}

type ReportSnapshot struct {
	Segments                        []SegmentSnapshot
	SeparateOutput, Ended, Complete bool
	ExitCode                        int
}

type invocationSegment struct {
	facts      SegmentObservation
	output     *feed.Output
	raw        []byte
	unretained int
	vcs        bool
}

type invocationReport struct {
	segments                                   []invocationSegment
	separate, terminal, lossy, ended, complete bool
	exit                                       int
}

// SegmentText supplies the already interpreted source for a stable report index.
func (i *Invocation) SegmentText(index int) string {
	if index >= 0 && index < len(i.report.segments) {
		return i.report.segments[index].facts.Text
	}
	return ""
}

// ObserveSegments retains drained bytes and reduces source facts independently
// of terminal reveal. Loss requests release at the external observation owner.
// Source: apps/mekugi/internal/router/exec_track.go:222:313@1bcbdd5a.
func (i *Invocation) ObserveSegments(report SegmentReport) (changed, loss bool) {
	r := &i.report
	lossy := r.lossy || report.Lossy
	for len(r.segments) < len(report.Segments) {
		r.segments = append(r.segments, invocationSegment{})
	}
	for index, observed := range report.Segments {
		segment := &r.segments[index]
		facts := observed
		facts.Output = ""
		changed = changed || segment.facts != facts
		segment.facts = facts
		segment.vcs = VCSOutputSource(observed.Analysis)
		if lossy {
			continue
		}
		if segment.vcs {
			segment.raw = append(segment.raw, observed.Output...)
		}
		if i.retention == nil {
			segment.unretained += len(observed.Output)
		}
		if max(len(segment.raw), segment.unretained, len(observed.Output)) > feed.OutputBytes {
			lossy, loss = true, true
			segment.raw = nil
		}
	}
	complete := report.Ended && report.Closed && report.Done
	separate := !report.Terminal && len(report.Segments) != 1 && !lossy && !(report.Ended && !complete)
	changed = changed || r.separate != separate || r.terminal != report.Terminal || r.lossy != lossy || r.ended != report.Ended || r.complete != complete || r.exit != report.ExitCode
	r.separate, r.terminal, r.lossy, r.ended, r.complete, r.exit = separate, report.Terminal, lossy, report.Ended, complete, report.ExitCode
	for index, observed := range report.Segments {
		segment := &r.segments[index]
		if !separate {
			segment.unretained = 0
			if segment.output != nil {
				segment.output.Release()
				segment.output = nil
			}
			continue
		}
		if observed.Began && i.retention != nil {
			if segment.output == nil {
				segment.output = i.retention.New()
			}
			segment.output.Write(observed.Output)
			if observed.Finished {
				segment.output.Finish(nil, &observed.ExitCode)
			}
		}
	}
	return changed, loss
}

// ReportSnapshot projects admitted segment boundaries. Final projection marks
// unreached segments skipped and closes a segment whose shell exited within it.
func (i *Invocation) ReportSnapshot(final bool) ReportSnapshot {
	r := &i.report
	view := ReportSnapshot{SeparateOutput: r.separate, Ended: r.ended, Complete: r.complete, ExitCode: r.exit}
	last := -1
	for index, segment := range r.segments {
		if segment.facts.Began || segment.facts.Finished {
			last = index
		}
	}
	for index := range r.segments {
		segment := &r.segments[index]
		fact := segment.facts
		shown := SegmentSnapshot{CommandSegment: CommandSegment{Source: fact.Source, Text: fact.Text, Started: fact.Started, Ended: fact.Ended, Duration: fact.Duration, ExitCode: fact.ExitCode}}
		switch {
		case fact.Began && !fact.Finished && final:
			shown.ExitCode = r.exit
		case fact.Began && !fact.Finished:
			shown.Running = true
		case !fact.Began && (final || index < last):
			shown.Skipped = true
		case !fact.Began:
			continue
		}
		if segment.output != nil {
			if !shown.Running {
				segment.output.Finish(nil, &shown.ExitCode)
			}
			output, _ := segment.output.Context(0)
			shown.Output, shown.OutputContent = &output, segment.output.HasContent()
		}
		if final && r.separate && !shown.Skipped && shown.ExitCode == 0 && segment.vcs {
			shown.Raw = string(segment.raw)
		}
		view.Segments = append(view.Segments, shown)
	}
	return view
}

func (i *Invocation) ReportMatchesHost() bool {
	return i.complete && i.exited && i.report.complete && i.report.exit == i.exit && len(i.report.segments) > 0
}

// SegmentOutput binds an existing segment index to private retained content.
// Detached publications expose only its snapshot and Output=index+2 identity.
func (i *Invocation) SegmentOutput(index int) *feed.Output {
	if index < 0 || index >= len(i.report.segments) {
		return nil
	}
	return i.report.segments[index].output
}

func (i *Invocation) ReleaseSegmentOutputs() bool {
	released := false
	for index := range i.report.segments {
		segment := &i.report.segments[index]
		if segment.output != nil {
			segment.output.Release()
			segment.output = nil
			released = true
		}
	}
	return released
}
