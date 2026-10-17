package sessionpresentation

import (
	"time"

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
