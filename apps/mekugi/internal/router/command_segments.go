package router

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

const commandSegmentRetentionLimit = 32

type commandSegmentWrite struct {
	key [3]string // Original thread, turn and item, independent of the current UI.
	err error
}

func (u *appServerUI) retainCommandSegments(entry activityPaneEntry, item appServerItem, view execTrackView) {
	if u.segments == nil || u.proxy == nil || u.proxy.replayStore == nil || item.AggregatedOutput == nil || item.ExitCode == nil {
		return
	}
	script, ok := appServerShellScript(item.Command)
	parts, split := shellinstrumentation.Split(script)
	if !ok || !split || len(parts) != len(view.segments) || !view.complete || view.code != *item.ExitCode {
		return
	}
	if u.commandSegmentPending == commandSegmentRetentionLimit {
		u.commandSegmentRetentionFailed([3]string{entry.native.thread, entry.native.turn, entry.native.item}, fmt.Errorf("pending command segment reports reached the limit of %d", commandSegmentRetentionLimit))
		return
	}
	record := &reports.Report{Command: item.Command, Exit: *item.ExitCode, Output: sha256.Sum256([]byte(*item.AggregatedOutput))}
	for i, segment := range view.segments {
		part := reports.Part{Timing: reports.Timing(segment.timing), Source: parts[i].Source, Skipped: segment.skipped, Exit: segment.exit}
		if view.output && segment.output != nil {
			output := segment.output.View()
			if output.Done && !output.Released && !output.Truncated && output.Dropped == 0 {
				part.Output = new(strings.Join(output.Lines, "\n"))
				if segment.raw != "" {
					// VCS parsers need original delimiters (not display-expanded
					// tabs). Keep that complete evidence as the retained output.
					part.Output = new(segment.raw)
				}
			}
		}
		record.Parts = append(record.Parts, part)
	}
	// Snapshot every UI-owned value before leaving the event loop. The shared
	// store can be busy with capture or another session; that must neither
	// freeze presentation nor discard a report after a one-second lock wait.
	ctx, cancel := context.WithTimeout(u.ctx, 30*time.Second)
	id := reports.ID(entry.native.turn, entry.native.item)
	key := [3]string{entry.native.thread, entry.native.turn, entry.native.item}
	store := u.proxy.replayStore
	scope := reports.Scope{Store: store.publication, Workspace: u.session.cwd, Thread: entry.native.thread, Limits: store.retentionLimits(), Writes: store.writes}
	if u.commandSegmentWrites == nil {
		u.commandSegmentWrites = make(chan commandSegmentWrite, commandSegmentRetentionLimit)
	}
	writes := u.commandSegmentWrites
	u.commandSegmentPending++
	go func() {
		defer cancel()
		writes <- commandSegmentWrite{key: key, err: storagePublicationError(u.segments.Publish(ctx, scope, id, *record))}
	}()
}

// Only the UI event loop consumes results or changes presentation state.
func (u *appServerUI) commandSegmentRetained(result commandSegmentWrite) {
	u.commandSegmentPending--
	if result.err == nil {
		return
	}
	u.commandSegmentRetentionFailed(result.key, result.err)
}

func (u *appServerUI) commandSegmentRetentionFailed(key [3]string, err error) {
	if u.issues == nil {
		return
	}
	message := fmt.Sprintf("Command segment history could not be retained for item %s (turn %s). Live output remains available, but this report cannot be restored after restart. Error: %v", key[2], key[1], err)
	if diagnostic, ok := errors.AsType[*criticalDiagnosticError](err); ok && !strings.Contains(message, diagnostic.summary) {
		message += "\n" + diagnostic.summary
	}
	// Use the native notice owner for full, wrapped transcript errors and
	// launcher recovery. Distinct items and causes must not collapse together.
	category := fmt.Sprintf("command_segment_retention:%x", sha256.Sum256([]byte(message)))
	u.issues.addThreadNotice("", key[0], category, message)
	u.dirty = true
}

// Normal exit drains accepted reports before releasing the session's resources.
// Cancellation still reaches each bounded store operation through u.ctx.
func (u *appServerUI) finishCommandSegments() {
	for u.commandSegmentPending > 0 {
		u.commandSegmentRetained(<-u.commandSegmentWrites)
	}
}

// Restore only real, terminal observations that agree with visible host
// history. Missing, corrupt or mismatched records leave the aggregate intact.
func (u *appServerUI) restoreCommandSegments(entry *activityPaneEntry, item appServerItem, workspace string) {
	if item.Type != "commandExecution" || item.ExitCode == nil || item.AggregatedOutput == nil || u.proxy == nil || u.proxy.replayStore == nil {
		return
	}
	var record *reports.Report
	if u.restoredSegments != nil {
		record = u.restoredSegments[entry.native.item]
	} else {
		item.ID = entry.native.item
		record = u.prepareCommandSegments(entry.native.thread, workspace, appServerHistoryTurn{ID: entry.native.turn, Items: []appServerItem{item}})[item.ID]
	}
	if record == nil {
		return
	}
	separate := true
	for _, part := range record.Parts {
		separate = separate && (part.Skipped || part.Output != nil)
	}
	cwd := cmp.Or(entry.native.commandCwd, appServerCommandDirectory(item, workspace))
	for _, part := range record.Parts {
		segment := commandSegment{timing: segmenttransport.Timing(part.Timing), source: part.Source, text: execSegmentTextInDirectory(part.Source, cwd), skipped: part.Skipped, exit: part.Exit}
		if separate && !part.Skipped {
			if outputs := u.session.activity.Outputs(&u.session.state); outputs != nil {
				segment.output = outputs.New()
				segment.output.Finish(part.Output, &part.Exit)
			}
			segment.tail, segment.omit = appServerOutputTail(part.Output)
			if part.Exit == 0 {
				segment.changes, segment.commit = commandOutputRows(appServerItem{Command: part.Source, Cwd: vcsSegmentCwd(item.Cwd, entry.native.segments), AggregatedOutput: part.Output})
			}
		}
		entry.native.segments = append(entry.native.segments, segment)
	}
	entry.native.command = item.Command
	entry.native.running, entry.native.collapsed = false, true
	entry.outputTail, entry.outputOmit = nil, 0
	if !separate {
		entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
	}
}

// Decode host facts before the report owner validates and adopts each batch.
func (u *appServerUI) prepareCommandSegments(thread, workspace string, turn appServerHistoryTurn) map[string]*reports.Report {
	result := make(map[string]*reports.Report)
	if u.segments == nil || u.proxy == nil || u.proxy.replayStore == nil {
		return result
	}
	store := u.proxy.replayStore
	scope := reports.Scope{Store: store.publication, Workspace: workspace, Thread: thread, Limits: store.retentionLimits(), Writes: store.writes}
	for items := range slices.Chunk(turn.Items, 64) {
		var completed []reports.Completed
		for _, item := range items {
			if item.Type != "commandExecution" || item.ExitCode == nil || item.AggregatedOutput == nil {
				continue
			}
			script, ok := appServerShellScript(item.Command)
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
		ctx, cancel := context.WithTimeout(u.ctx, time.Second)
		accepted, _ := u.segments.Restore(ctx, scope, completed)
		cancel()
		for _, item := range items {
			if record := accepted[reports.ID(turn.ID, item.ID)]; record != nil {
				result[item.ID] = record
			}
		}
	}
	return result
}
