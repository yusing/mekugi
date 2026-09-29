package router

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
)

// Completed segment observations share the managed replay store's atomic
// publication, workspace scope and retention. They are not execution receipts.
type retainedCommandSegments struct {
	Command string
	Exit    int
	Output  [32]byte // Bind the report to the host aggregate, without a second copy.
	Parts   []retainedCommandSegment
}

type retainedCommandSegment struct {
	Timing  execsegment.Timing `json:",omitzero"`
	Source  string
	Skipped bool
	Exit    int
	Output  *string // nil means no complete per-segment output was retained.
}

// A fork inherits host turn/item identities. Matching that exact completed
// item permits replay without borrowing a live parent's process or state.
func commandSegmentsID(turn, item string) string {
	return fmt.Sprintf("command-segments:%x", sha256.Sum256(fmt.Appendf(nil, "%q:%q", turn, item)))
}

func (u *appServerUI) retainCommandSegments(entry activityPaneEntry, item appServerItem, view execTrackView) {
	if u.proxy == nil || u.proxy.replayStore == nil || item.AggregatedOutput == nil || item.ExitCode == nil {
		return
	}
	script, ok := appServerShellScript(item.Command)
	parts, split := execsegment.Split(script)
	if !ok || !split || len(parts) != len(view.segments) || !view.complete || view.code != *item.ExitCode {
		return
	}
	record := &retainedCommandSegments{Command: item.Command, Exit: *item.ExitCode, Output: sha256.Sum256([]byte(*item.AggregatedOutput))}
	for i, segment := range view.segments {
		part := retainedCommandSegment{Timing: segment.timing, Source: parts[i].Source, Skipped: segment.skipped, Exit: segment.exit}
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
	ctx, cancel := context.WithTimeout(u.ctx, time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: entry.native.thread})
	id := commandSegmentsID(entry.native.turn, entry.native.item)
	if err := u.proxy.replayStore.put(ctx, u.session.cwd, map[string]mekugiHistory{id: {CommandSegments: record}}); err != nil {
		u.setNotice("Command segment history could not be retained; live output remains available.", true)
	}
}

// Restore only real, terminal observations that agree with visible host
// history. Missing, corrupt or mismatched records leave the aggregate intact.
func (u *appServerUI) restoreCommandSegments(entry *activityPaneEntry, item appServerItem, workspace string) {
	if item.Type != "commandExecution" || item.ExitCode == nil || item.AggregatedOutput == nil || u.proxy == nil || u.proxy.replayStore == nil {
		return
	}
	script, ok := appServerShellScript(item.Command)
	parts, split := execsegment.Split(script)
	if !ok || !split {
		return
	}
	ctx, cancel := context.WithTimeout(u.ctx, time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: entry.native.thread})
	store := u.proxy.replayStore.scoped(ctx)
	id := commandSegmentsID(entry.native.turn, entry.native.item)
	var record *retainedCommandSegments
	err := store.locked(ctx, func() error {
		r, found, err := store.read(workspace, id, false)
		if err != nil || !found {
			return err
		}
		candidate := r.History.CommandSegments
		if candidate == nil || candidate.Command != item.Command || candidate.Exit != *item.ExitCode || candidate.Output != sha256.Sum256([]byte(*item.AggregatedOutput)) || len(candidate.Parts) != len(parts) {
			return nil
		}
		for i, part := range candidate.Parts {
			if part.Source != parts[i].Source || part.Skipped && (part.Output != nil || part.Exit != 0 || part.Timing != (execsegment.Timing{})) {
				return nil
			}
			timing := part.Timing
			if timing.ElapsedNS < 0 || timing.Started.IsZero() && (!timing.Ended.IsZero() || timing.ElapsedNS != 0) || timing.Ended.IsZero() && timing.ElapsedNS != 0 {
				return nil
			}
		}
		if err := store.retainFiles(replayRecordName(workspace, id, false)); err != nil {
			return err
		}
		record = candidate
		return nil
	})
	if err != nil || record == nil {
		return
	}
	separate := true
	for _, part := range record.Parts {
		separate = separate && (part.Skipped || part.Output != nil)
	}
	for _, part := range record.Parts {
		segment := commandSegment{timing: part.Timing, source: part.Source, text: execSegmentText(part.Source), skipped: part.Skipped, exit: part.Exit}
		if separate && !part.Skipped {
			segment.output = u.session.outputs.New()
			segment.output.Finish(part.Output, &part.Exit)
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
