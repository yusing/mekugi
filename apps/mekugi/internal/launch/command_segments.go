package launch

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

const commandSegmentRetentionLimit = 32

type commandSegmentWrite struct {
	key [3]string // Original thread, turn and item, independent of the current UI.
	err error
}

func retainCommandSegments(parent context.Context, retained changes.Scope, segments *reports.Service, attached *attachedPresentation, writes *chan commandSegmentWrite, count *int, issues *requestexecution.Failures, entry sessionpresentation.ActivityEntry, item observation.Item, view execTrackView) {
	if segments == nil || retained.Store == nil || item.AggregatedOutput == nil || item.ExitCode == nil {
		return
	}
	script, ok := shellinstrumentation.CommandScript(item.Command)
	parts, split := shellinstrumentation.Split(script)
	if !ok || !split || len(parts) != len(view.segments) || !view.complete || view.code != *item.ExitCode {
		return
	}
	if (*count) == commandSegmentRetentionLimit {
		commandSegmentRetentionFailed(issues, [3]string{entry.Native.Thread, entry.Native.Turn, entry.Native.Item}, fmt.Errorf("pending command segment reports reached the limit of %d", commandSegmentRetentionLimit))
		return
	}
	record := &reports.Report{Command: item.Command, Exit: *item.ExitCode, Output: sha256.Sum256([]byte(*item.AggregatedOutput))}
	for i, segment := range view.segments {
		part := reports.Part{Timing: reports.Timing(segment.Timing), Source: parts[i].Source, Skipped: segment.Skipped, Exit: segment.Exit}
		if view.output && segment.Output != nil {
			output := segment.Output.View()
			if output.Done && !output.Released && !output.Truncated && output.Dropped == 0 {
				part.Output = new(strings.Join(output.Lines, "\n"))
				if segment.Raw != "" {
					// VCS parsers need original delimiters (not display-expanded
					// tabs). Keep that complete evidence as the retained output.
					part.Output = new(segment.Raw)
				}
			}
		}
		record.Parts = append(record.Parts, part)
	}
	// Snapshot every UI-owned value before leaving the event loop. The shared
	// store can be busy with capture or another session; that must neither
	// freeze presentation nor discard a report after a one-second lock wait.
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	id := reports.ID(entry.Native.Turn, entry.Native.Item)
	key := [3]string{entry.Native.Thread, entry.Native.Turn, entry.Native.Item}
	store := retained
	scope := reports.Scope{Store: store.Store, Workspace: attached.cwd, Thread: entry.Native.Thread, Limits: store.Limits, Writes: store.Writes}
	if (*writes) == nil {
		(*writes) = make(chan commandSegmentWrite, commandSegmentRetentionLimit)
	}
	delivery := *writes
	(*count)++
	go func() {
		defer cancel()
		delivery <- commandSegmentWrite{key: key, err: requestexecution.StoragePublicationError(segments.Publish(ctx, scope, id, *record))}
	}()
}

// Only the UI event loop consumes results or changes presentation state.
func commandSegmentRetained(count *int, issues *requestexecution.Failures, result commandSegmentWrite) {
	(*count)--
	if result.err == nil {
		return
	}
	commandSegmentRetentionFailed(issues, result.key, result.err)
}

func commandSegmentRetentionFailed(issues *requestexecution.Failures, key [3]string, err error) bool {
	if issues == nil {
		return false
	}
	message := fmt.Sprintf("Command segment history could not be retained for item %s (turn %s). Live output remains available, but this report cannot be restored after restart. Error: %v", key[2], key[1], err)
	if diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](err); ok && !strings.Contains(message, diagnostic.Summary) {
		message += "\n" + diagnostic.Summary
	}
	// Use the native notice owner for full, wrapped transcript errors and
	// launcher recovery. Distinct items and causes must not collapse together.
	category := fmt.Sprintf("command_segment_retention:%x", sha256.Sum256([]byte(message)))
	issues.AddThreadNotice("", key[0], category, message)
	return true
}

// Normal exit drains accepted reports before releasing the session's resources.
// Cancellation still reaches each bounded store operation through its parent context.
func finishCommandSegments(writes *chan commandSegmentWrite, count *int, issues *requestexecution.Failures) {
	for (*count) > 0 {
		commandSegmentRetained(count, issues, <-(*writes))
	}
}

// Restore only real, terminal observations that agree with visible host
// history. Missing, corrupt or mismatched records leave the aggregate intact.
func restoreCommandSegments(parent context.Context, retained changes.Scope, segments *reports.Service, attached *attachedPresentation, activity *activitymodel.Service, restored map[string]*reports.Report, entry *sessionpresentation.ActivityEntry, item binding.ThreadItem, workspace string) {
	if item.Type != "commandExecution" || item.ExitCode == nil || item.AggregatedOutput == nil || retained.Store == nil {
		return
	}
	var record *reports.Report
	if restored != nil {
		record = restored[entry.Native.Item]
	} else {
		item.ID = entry.Native.Item
		record = prepareCommandSegments(parent, retained, segments, entry.Native.Thread, workspace, binding.HistoryTurn{ID: entry.Native.Turn, Items: []binding.ThreadItem{item}})[item.ID]
	}
	if record == nil {
		return
	}
	separate := true
	for _, part := range record.Parts {
		separate = separate && (part.Skipped || part.Output != nil)
	}
	cwd := cmp.Or(entry.Native.CommandCwd, sessionpresentation.CommandDirectory(item.Observation(), workspace))
	for _, part := range record.Parts {
		segment := sessionpresentation.ActivitySegment{Timing: segmenttransport.Timing(part.Timing), Source: part.Source, Text: (activitypresentation.CommandSource{Script: part.Source, Display: frontends.ShellDisplaySource(part.Source), Directory: cwd}).SegmentText(), Skipped: part.Skipped, Exit: part.Exit}
		if separate && !part.Skipped {
			if outputs := activity.Outputs(attached.session.Scope()); outputs != nil {
				segment.Output = outputs.New()
				segment.Output.Finish(part.Output, &part.Exit)
			}
			segment.Tail, segment.Omit = sessionpresentation.OutputTail(part.Output)
			if part.Exit == 0 {
				segment.Changes, segment.Commit = sessionpresentation.CommandOutputRows(observation.Item{Command: part.Source, Cwd: vcsSegmentCwd(item.Cwd, entry.Native.Segments), AggregatedOutput: part.Output})
			}
		}
		entry.Native.Segments = append(entry.Native.Segments, segment)
	}
	entry.Native.Command = item.Command
	entry.Native.Running = false
	entry.OutputTail, entry.OutputOmit = nil, 0
	if !separate {
		entry.OutputTail, entry.OutputOmit = sessionpresentation.OutputTail(item.AggregatedOutput)
	}
}

// Decode host facts before the report owner validates and adopts each batch.
func prepareCommandSegments(parent context.Context, retained changes.Scope, segments *reports.Service, thread, workspace string, turn binding.HistoryTurn) map[string]*reports.Report {
	result := make(map[string]*reports.Report)
	if segments == nil || retained.Store == nil {
		return result
	}
	store := retained
	scope := reports.Scope{Store: store.Store, Workspace: workspace, Thread: thread, Limits: store.Limits, Writes: store.Writes}
	for items := range slices.Chunk(turn.Items, 64) {
		var completed []reports.Completed
		for _, item := range items {
			if item.Type != "commandExecution" || item.ExitCode == nil || item.AggregatedOutput == nil {
				continue
			}
			script, ok := shellinstrumentation.CommandScript(item.Command)
			parts, split := shellinstrumentation.Split(script)
			if !ok || !split {
				continue
			}
			candidate := reports.Completed{ID: reports.ID(turn.ID, item.ID), Command: item.Command, Exit: *item.ExitCode, Output: sha256.Sum256([]byte(*item.AggregatedOutput))}
			for _, part := range parts {
				candidate.Sources = append(candidate.Sources, part.Source)
			}
			completed = append(completed, candidate)
		}
		if len(completed) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, time.Second)
		accepted, _ := segments.Restore(ctx, scope, completed)
		cancel()
		for _, item := range items {
			if record := accepted[reports.ID(turn.ID, item.ID)]; record != nil {
				result[item.ID] = record
			}
		}
	}
	return result
}

// releaseSegmentOutputs drops presentation retention without cancelling host reports.
func releaseSegmentOutputs(attached *attachedPresentation, observer *commandsegments.Observer) {
	for key, run := range attached.session.Commands() {
		if run.Entry.Native.Invocation.ReleaseSegmentOutputs() {
			observer.ReleaseOutput(key)
		}
	}
}
