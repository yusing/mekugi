package router

import (
	"context"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
)

// The transcript result binds a shell report even for background commands,
// whose final task output is not the launch result saved in native history.
type nativeCommandResult struct {
	Failed bool
	Caller string
}

type runtimeCommandResult struct {
	nativeCommandResult
	Output [32]byte
}

func (u *appServerUI) retainRuntimeCommandSegments(entry activityPaneEntry, view execTrackView) {
	native := entry.native
	if native == nil || native.commandResult == nil || !view.complete || (view.code != 0) != (native.status == "failed") {
		return
	}
	parts, ok := execsegment.Split(native.command)
	if !ok || len(parts) != len(view.segments) {
		return
	}
	ctx, _, err := u.runtimeOutputScope(native.item)
	if err != nil {
		return // No authenticated observation scope means no retained evidence.
	}
	record := commandSegmentRecord(native.command, parts, view, native.commandResult.Output)
	identity := native.commandResult.nativeCommandResult
	record.NativeResult = &identity
	// Native forks preserve tool-use IDs, not message/turn IDs. An authenticated
	// fork-source receipt permits adoption from that source session, with exact
	// workspace, command, result and caller checks on the visible native item.
	u.writeCommandSegments(ctx, u.runtime.observations.owner.store, u.session.cwd,
		"claude/"+commandSegmentsID(native.thread, native.item), [3]string{native.thread, "", native.item}, record)
}

func (u *appServerUI) restoreRuntimeCommandSegments(id string) {
	var original *liveActivityRecord
	for i := range u.view.entries {
		entry := &u.view.entries[i]
		if entry.CallID == id && entry.native != nil && entry.native.commandResult != nil && entry.native.command != "" {
			original = entry
			break
		}
	}
	if original == nil {
		return
	}
	native := original.native
	if len(native.segments) > 0 && native.thread == u.thread {
		return // A repeated native init must preserve an open saved dialog.
	}
	parts, ok := execsegment.Split(native.command)
	if !ok {
		return
	}
	ctx, call, err := u.runtimeOutputScope(strings.TrimPrefix(id, "history/"))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	store := u.runtime.observations.owner.store.scoped(ctx)
	var record *retainedCommandSegments
	err = store.locked(ctx, func() error {
		binding := call.Binding
		var dependencies []string
		for depth := 0; depth < 128; depth++ {
			key := "claude/" + commandSegmentsID(binding.Session, call.ID)
			r, found, err := store.read(u.session.cwd, key, false)
			if err != nil {
				return err
			}
			if found {
				candidate := r.History.CommandSegments
				result := native.commandResult
				if candidate == nil || candidate.NativeResult == nil || *candidate.NativeResult != result.nativeCommandResult ||
					candidate.Command != native.command || candidate.Output != result.Output || !validCommandSegmentParts(candidate, parts) {
					return nil
				}
				dependencies = append(dependencies, replayRecordName(u.session.cwd, key, false))
				if err := store.retainFiles(dependencies...); err != nil {
					return err
				}
				record = candidate
				return nil
			}
			key = observationThread(binding) + "/history-selection"
			origin, found, err := store.read(u.session.cwd, key, false)
			if err != nil || !found {
				return err
			}
			if origin.History.Root != u.session.cwd || origin.History.ExecutingThread != observationThread(binding) || !validNativeHistorySelection(binding, origin.History.NativeHistory) {
				return nil
			}
			dependencies = append(dependencies, replayRecordName(u.session.cwd, key, false))
			binding = origin.History.NativeHistory.Source
		}
		return nil
	})
	if err != nil {
		u.setNotice("Saved native command segments unavailable: "+err.Error(), true)
		return
	}
	if record == nil {
		return
	}
	entry := original.activityPaneEntry
	copy := *native
	copy.thread = u.thread
	entry.native = &copy
	// Keep the native aggregate when any segment lacks complete output.
	aggregate := strings.Join(entry.outputTail, "\n")
	u.restoreRetainedCommandSegments(&entry, appServerItem{Command: native.command, Cwd: u.session.cwd, AggregatedOutput: &aggregate}, record, u.session.cwd)
	for _, part := range record.Parts {
		if !part.Skipped && part.Output == nil {
			entry.outputTail, entry.outputOmit = original.outputTail, original.outputOmit
			break
		}
	}
	if record.Exit != 0 {
		entry.native.status = "failed"
	}
	for _, v := range []*liveActivityView{u.view, u.agents} {
		for i, old := range v.entries {
			if old.CallID == id {
				updated := entry
				updated.Seq, updated.Observed, updated.Agent = old.Seq, old.Observed, old.Agent
				v.replaceEntry(i, updated, parseLiveActivity(updated))
				break
			}
		}
	}
}

// A query-created fork loads inherited history before it gets a new session
// identity. Restore it only after that identity has an authenticated scope.
func (u *appServerUI) restoreRuntimeHistorySegments() {
	for _, entry := range u.view.entries {
		if strings.HasPrefix(entry.CallID, "history/") {
			u.restoreRuntimeCommandSegments(entry.CallID)
		}
	}
}
