package router

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type retainedHookObservation struct {
	Turn      string
	Run       appServerHookRun
	At        time.Time
	Output    string
	HasError  bool
	Completed bool
}

type retainedHookObservations struct {
	Version   int
	Workspace string
	Thread    string
	Dropped   int
	Runs      []retainedHookObservation
	bytes     int // Published input size, charged to offline replay's corpus bound.
}

func hookObservationsName(workspace, thread string) string {
	return fmt.Sprintf("hooks-%x.json", sha256.Sum256([]byte(workspace+"\x00"+thread)))
}

// Offline readers use published files only, without opening a writable store
// or acquiring resources. The same reader runs under store.lock for publication.
func (s *mekugiReplayStore) readHookObservations(workspace, thread string) (retainedHookObservations, error) {
	r := retainedHookObservations{Version: 1, Workspace: workspace, Thread: thread}
	if s == nil || s.directory == "" {
		return r, nil
	}
	path := filepath.Join(s.directory, hookObservationsName(workspace, thread))
	data, err := readManagedOutputFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r, json.RejectUnknownMembers(true)); err != nil {
		return r, err
	}
	r.bytes = len(data)
	if r.Version != 1 || r.Workspace != workspace || r.Thread != thread {
		return r, errors.New("hook observation identity mismatch")
	}
	for i, run := range r.Runs {
		if run.Run.ID == "" || run.At.IsZero() {
			return r, errors.New("hook observation lacks identity or time")
		}
		// Earlier releases kept the status message as the first output line.
		// Host entries never use that label.
		if line, ok := strings.CutPrefix(run.Output, "status: "); ok && run.Run.StatusMessage == "" {
			r.Runs[i].Run.StatusMessage, r.Runs[i].Output, _ = strings.Cut(line, "\n")
		}
	}
	return r, nil
}

func (s *appServerSession) retainHook(thread string, observation retainedHookObservation) error {
	store := s.waitStore // Existing native-session managed store and lease.
	if store == nil {
		return errors.New("native session store is unavailable")
	}
	return store.locked(s.waitContext, func() error {
		r, err := store.readHookObservations(s.cwd, thread)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(r.Runs, func(old retainedHookObservation) bool { return old.Run.ID == observation.Run.ID })
		if index >= 0 {
			if r.Runs[index].Completed || !observation.Completed {
				return nil
			}
			r.Runs = append(slices.Delete(r.Runs, index, index+1), observation)
		} else {
			r.Runs = append(r.Runs, observation)
		}
		for {
			data, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if len(data) <= maxReplayRecordBytes {
				return store.writeManagedFile(hookObservationsName(s.cwd, thread), "hooks-pending-", data)
			}
			if len(r.Runs) < 2 {
				return errors.New("hook observation exceeds size limit")
			}
			// Keep the latest observation and disclose the bounded history gap.
			r.Runs = slices.Delete(r.Runs, 0, 1)
			r.Dropped++
		}
	})
}

func (u *appServerUI) restoredHookEvents(info appServerThreadInfo) []restoredRolloutEvent {
	store := u.session.waitStore
	r, err := store.readHookObservations(u.session.cwd, info.ID)
	if err != nil {
		u.restoreContentNotice("Hook observations for " + info.ID + " unavailable: " + err.Error())
		return nil
	}
	if r.Dropped > 0 {
		u.restoreContentNotice(fmt.Sprintf("%s: %d earlier hook observations were not retained", info.ID, r.Dropped))
	}
	if store != nil && len(r.Runs) > 0 {
		if err := store.locked(u.session.waitContext, func() error {
			return store.retainFiles(hookObservationsName(u.session.cwd, info.ID))
		}); err != nil {
			u.restoreContentNotice("Hook observations for " + info.ID + " could not be retained: " + err.Error())
		}
	}
	var events []restoredRolloutEvent
	for _, observation := range r.Runs {
		entry := u.session.hookEntry(info.ID, observation, false)
		// Position by time, including thread-scoped hooks outside any turn.
		events = append(events, restoredRolloutEvent{at: observation.At, entry: entry})
	}
	return events
}
