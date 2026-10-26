package sessionpresentation

import (
	"strings"
	"time"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
)

// CommandOperation correlates admitted host facts with the same Invocation held
// by feed records. It has no terminal cursor, process or retained-store capability.
// Source: apps/mekugi/internal/router/app_server_events.go:62:71@29a3a244.
type CommandOperation struct {
	Entry       ActivityEntry
	Completion  *ActivityEntry
	Completed   observation.Item
	CompletedAt time.Time
}

var commandOperations scope.Key[map[[3]string]*CommandOperation]

func (a *Attachment) Commands() map[[3]string]*CommandOperation {
	value := commandOperations.Value(a.Scope())
	if *value == nil {
		*value = make(map[[3]string]*CommandOperation)
	}
	return *value
}

// CommandReport supplies source interpretation once, while every observation
// keeps its newly drained bytes. Invocation owns the admitted segment facts.
// Source: apps/mekugi/internal/router/exec_track.go:50:77@29a3a244.
func CommandReport(invocation *activitypresentation.Invocation, track segments.Report, directory string) activitypresentation.SegmentReport {
	report := activitypresentation.SegmentReport{Terminal: track.Terminal, Lossy: track.Lossy, Ended: track.Ended, Closed: track.Closed, Done: track.Done, ExitCode: track.Code}
	for index, observed := range track.Segments {
		analysis := frontends.ShellDisplaySource(observed.Source)
		text := invocation.SegmentText(index)
		if text == "" {
			text = (activitypresentation.CommandSource{Script: observed.Source, Display: analysis, Directory: directory}).SegmentText()
		}
		report.Segments = append(report.Segments, activitypresentation.SegmentObservation{
			Source: observed.Source, Text: text, Analysis: analysis,
			Started: observed.Timing.Started, Ended: observed.Timing.Ended, Duration: time.Duration(observed.Timing.ElapsedNS),
			Began: observed.Began, Finished: observed.Ended, ExitCode: observed.Code, Output: observed.Output,
		})
	}
	return report
}

// CommandReportState holds the canonical reduction and a detached reveal input.
// Private retained outputs occur only in Segments, never Snapshot.
type CommandReportState struct {
	Snapshot         activitypresentation.ReportSnapshot
	Segments         []ActivitySegment
	Changed, Release bool
}

// ObserveCommandReport admits drained evidence independently of terminal pacing.
// Source: apps/mekugi/internal/router/exec_track.go:39:63@7166de95.
func ObserveCommandReport(invocation *activitypresentation.Invocation, report activitypresentation.SegmentReport, final bool) CommandReportState {
	changed, release := invocation.ObserveSegments(report)
	snapshot := invocation.ReportSnapshot(final)
	state := CommandReportState{Snapshot: snapshot, Changed: changed, Release: release}
	for index, segment := range snapshot.Segments {
		shown := ActivitySegment{Timing: segmenttransport.Timing{Started: segment.Started, Ended: segment.Ended, ElapsedNS: int64(segment.Duration)},
			Text: segment.Text, Source: segment.Source, Running: segment.Running, Skipped: segment.Skipped, Pending: segment.Pending, Exit: segment.ExitCode, Raw: segment.Raw,
			Output: invocation.SegmentOutput(index), Results: segment.Results}
		if snapshot.SeparateOutput {
			shown.Tail, shown.Omit, shown.Clipped = segment.Tail, segment.TailOmitted, segment.TailClipped
			if final && !shown.Skipped && shown.Exit == 0 {
				output := strings.Join(shown.Tail, "\n")
				shown.Changes = activitypresentation.MChangesOutputRows(DisplayCommand(shown.Source), &output)
				if len(shown.Changes) > 0 && shown.Omit > 0 {
					shown.Changes = append([]activitypresentation.ChangeRow{{OmittedLines: shown.Omit}}, shown.Changes...)
				}
			}
		}
		state.Segments = append(state.Segments, shown)
	}
	return state
}
