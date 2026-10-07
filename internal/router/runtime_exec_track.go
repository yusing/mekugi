package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// PrepareCommandTracking installs only this launch's Bash startup observer.
// The native shell still owns execution, sandboxing and terminal settlement.
func (s *ObservationService) PrepareCommandTracking(ctx context.Context, helper, previous string) (string, error) {
	if helper == "" {
		return "", nil
	}
	socket, directory := ExecTrackPaths(filepath.Join(s.directory, "bin"))
	ctx, cancel := context.WithCancel(ctx)
	hub, err := listenExecTrack(ctx, socket, directory)
	if err != nil {
		cancel()
		return "", err
	}
	s.trackCancel, s.owner.execTrack = cancel, hub
	tracker := filepath.Join(s.directory, "exec-track.bash")
	if err := os.WriteFile(tracker, []byte(execsegment.ClaudeTracker(helper, socket, directory)), 0600); err != nil {
		return "", err
	}
	startup := ""
	if previous != "" {
		// Preserve caller startup effects without activating an older observer.
		// Nested command shells keep their guard; only a fresh shell activates
		// this launch's observer after the previous startup returns.
		source := ". " + shellsyntax.Quote(previous) + "\n"
		startup = "if [ -z \"${" + execsegment.Guard + "+x}\" ]; then\n" +
			"export " + execsegment.Guard + "=1\n" + source + "unset " + execsegment.Guard + "\nelse\n" + source + "fi\n"
	}
	startup += execsegment.ClaudeHook(tracker)
	path := filepath.Join(s.directory, "bash-env")
	return path, os.WriteFile(path, []byte(startup), 0600)
}

func (s *ObservationService) closeCommandTracking() error {
	if s.trackCancel == nil {
		return nil
	}
	socket, directory := ExecTrackPaths(filepath.Join(s.directory, "bin"))
	return errors.Join(removeObservationSocket(socket), os.RemoveAll(directory),
		os.Remove(filepath.Join(s.directory, "exec-track.bash")), os.Remove(filepath.Join(s.directory, "bash-env")))
}

// Reports can finish before native background-task completion. Do not settle
// presentation until both the shell report and the native terminal edge exist.
func (u *appServerUI) flushRuntimeCommandSegments() {
	if u.execTrack == nil {
		return
	}
	for _, key := range u.execTrack.nativeKeys(u.thread) {
		var old *liveActivityRecord
		for i := range u.view.entries {
			if entry := &u.view.entries[i]; entry.CallID == key[2] && entry.native != nil && entry.native.command != "" {
				old = entry
				break
			}
		}
		if old == nil {
			continue
		}
		final := !old.native.running
		report, changed := u.execTrack.view(key, false, execSegmentText, &u.session.outputs)
		if final && !report.ended {
			if u.now().Sub(old.native.commandEnded) < execTrackCompletionWait {
				continue
			}
			report.complete = false
		}
		fallback := (final || report.ended) && (!report.complete || final && (report.code != 0) != (old.native.status == "failed"))
		if fallback {
			report.segments, report.output = nil, false
		} else if final {
			report, _ = u.execTrack.view(key, true, execSegmentText, &u.session.outputs)
		} else if !changed {
			continue
		}
		for _, v := range []*liveActivityView{u.view, u.agents} {
			for i, entry := range v.entries {
				if entry.CallID != old.CallID || entry.native == nil {
					continue
				}
				updated := entry.activityPaneEntry
				native := *entry.native
				updated.native = &native
				native.segments = report.segments
				if report.output {
					updated.outputTail, updated.outputOmit = nil, 0
				}
				v.replaceEntry(i, updated, parseLiveActivity(updated))
				break
			}
		}
		u.dirty = true
		if final || fallback {
			u.execTrack.finish(key)
		}
	}
}
