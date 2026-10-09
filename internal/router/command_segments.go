package router

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
)

// Completed segment observations share the managed replay store's atomic
// publication, workspace scope and retention. They are not execution receipts.
type retainedCommandSegments struct {
	Command      string
	Exit         int
	Output       [32]byte // Bind the report to the host aggregate, without a second copy.
	Parts        []retainedCommandSegment
	NativeResult *nativeCommandResult `json:",omitempty"`
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

const commandSegmentRetentionLimit = 32

type commandSegmentWrite struct {
	key [3]string // Original thread, turn and item, independent of the current UI.
	err error
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
	record := commandSegmentRecord(item.Command, parts, view, sha256.Sum256([]byte(*item.AggregatedOutput)))
	ctx := context.WithValue(u.ctx, storageSessionKey{}, storageSessionIdentity{Thread: entry.native.thread})
	u.writeCommandSegments(ctx, u.proxy.replayStore, u.session.cwd, commandSegmentsID(entry.native.turn, entry.native.item),
		[3]string{entry.native.thread, entry.native.turn, entry.native.item}, record)
}

func commandSegmentRecord(command string, parts []execsegment.Segment, view execTrackView, output [32]byte) *retainedCommandSegments {
	record := &retainedCommandSegments{Command: command, Exit: view.code, Output: output}
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
	return record
}

func (u *appServerUI) writeCommandSegments(ctx context.Context, store *mekugiReplayStore, workspace, id string, key [3]string, record *retainedCommandSegments) {
	if u.commandSegmentPending == commandSegmentRetentionLimit {
		u.commandSegmentRetentionFailed(key, fmt.Errorf("pending command segment reports reached the limit of %d", commandSegmentRetentionLimit))
		return
	}
	// Snapshot every UI-owned value before leaving the event loop. The shared
	// store can be busy with capture or another session; that must neither
	// freeze presentation nor discard a report after a one-second lock wait.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if u.commandSegmentWrites == nil {
		u.commandSegmentWrites = make(chan commandSegmentWrite, commandSegmentRetentionLimit)
	}
	writes := u.commandSegmentWrites
	u.commandSegmentPending++
	go func() {
		defer cancel()
		writes <- commandSegmentWrite{key: key, err: store.put(ctx, workspace, map[string]mekugiHistory{id: {CommandSegments: record}})}
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
		u.issues = NewCriticalErrors()
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
	var record *retainedCommandSegments
	if u.restoredSegments != nil {
		record = u.restoredSegments[entry.native.item]
	} else {
		item.ID = entry.native.item
		record = u.prepareCommandSegments(entry.native.thread, workspace, appServerHistoryTurn{ID: entry.native.turn, Items: []appServerItem{item}})[item.ID]
	}
	if record == nil {
		return
	}
	u.restoreRetainedCommandSegments(entry, item, record, workspace)
}

func (u *appServerUI) restoreRetainedCommandSegments(entry *activityPaneEntry, item appServerItem, record *retainedCommandSegments, workspace string) {
	entry.native.segments = nil
	separate := true
	for _, part := range record.Parts {
		separate = separate && (part.Skipped || part.Output != nil)
	}
	cwd := cmp.Or(entry.native.commandCwd, appServerCommandDirectory(item, workspace))
	for _, part := range record.Parts {
		segment := commandSegment{timing: part.Timing, source: part.Source, text: commandSegmentText(entry.native, part.Source, cwd), skipped: part.Skipped, exit: part.Exit}
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

// Validate and adopt independent history records before exposing their segments.
// Batching avoids rewriting a growing catalog and rescanning storage per item.
func (u *appServerUI) prepareCommandSegments(thread, workspace string, turn appServerHistoryTurn) map[string]*retainedCommandSegments {
	result := make(map[string]*retainedCommandSegments)
	if u.proxy == nil || u.proxy.replayStore == nil {
		return result
	}
	const batchSize = 64
	for start := 0; start < len(turn.Items); start += batchSize {
		items := turn.Items[start:min(start+batchSize, len(turn.Items))]
		ctx, cancel := context.WithTimeout(u.ctx, time.Second)
		ctx = context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: thread})
		store := u.proxy.replayStore.scoped(ctx)
		_ = store.locked(ctx, func() error {
			accepted := make(map[string]*retainedCommandSegments)
			var names []string
			for _, item := range items {
				candidate, err := readCommandSegments(store, workspace, turn.ID, item)
				if err != nil || candidate == nil {
					continue
				}
				accepted[item.ID] = candidate
				names = append(names, replayRecordName(workspace, commandSegmentsID(turn.ID, item.ID), false))
			}
			if len(names) == 0 {
				return nil
			}
			if err := store.retainFiles(names...); err == nil {
				for id, candidate := range accepted {
					result[id] = candidate
				}
			} else {
				// A large combined catalog admission must not suppress valid
				// smaller neighbors that still fit, or records already owned.
				for _, item := range items {
					if candidate := accepted[item.ID]; candidate != nil {
						if err := store.retainFiles(replayRecordName(workspace, commandSegmentsID(turn.ID, item.ID), false)); err == nil {
							result[item.ID] = candidate
						}
					}
				}
			}
			return nil
		})
		cancel()
	}
	return result
}

// Called with the store locked. A malformed neighbor never prevents other reads.
func readCommandSegments(store *mekugiReplayStore, workspace, turn string, item appServerItem) (*retainedCommandSegments, error) {
	if item.Type != "commandExecution" || item.ExitCode == nil || item.AggregatedOutput == nil {
		return nil, nil
	}
	script, ok := appServerShellScript(item.Command)
	parts, split := execsegment.Split(script)
	if !ok || !split {
		return nil, nil
	}
	r, found, err := store.read(workspace, commandSegmentsID(turn, item.ID), false)
	if err != nil || !found {
		return nil, err
	}
	candidate := r.History.CommandSegments
	if candidate == nil || candidate.Command != item.Command || candidate.Exit != *item.ExitCode || candidate.Output != sha256.Sum256([]byte(*item.AggregatedOutput)) {
		return nil, nil
	}
	if !validCommandSegmentParts(candidate, parts) {
		return nil, nil
	}
	return candidate, nil
}

func validCommandSegmentParts(candidate *retainedCommandSegments, parts []execsegment.Segment) bool {
	if len(candidate.Parts) != len(parts) {
		return false
	}
	for i, part := range candidate.Parts {
		if part.Source != parts[i].Source || part.Skipped && (part.Output != nil || part.Exit != 0 || part.Timing != (execsegment.Timing{})) {
			return false
		}
		timing := part.Timing
		if timing.ElapsedNS < 0 || timing.Started.IsZero() && (!timing.Ended.IsZero() || timing.ElapsedNS != 0) || timing.Ended.IsZero() && timing.ElapsedNS != 0 {
			return false
		}
	}
	return true
}
