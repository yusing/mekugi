package router

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (s *appServerSession) retainHook(thread string, observation history.HookObservation) error {
	store := s.waitStore // Existing native-session managed store and lease.
	if store == nil {
		return errors.New("native session store is unavailable")
	}
	return store.locked(s.waitContext, func(store *mekugiReplayStore) error {
		r, err := store.publication.ReadHookObservations(s.cwd, thread)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(r.Runs, func(old history.HookObservation) bool { return old.Run.ID == observation.Run.ID })
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
				return store.writeManagedFile(history.HookObservationsName(s.cwd, thread), "hooks-pending-", data)
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
	var published *history.Store
	if store != nil {
		published = store.publication
	}
	r, err := published.ReadHookObservations(u.session.cwd, info.ID)
	if err != nil {
		u.restoreContentNotice("Hook observations for " + info.ID + " unavailable: " + err.Error())
		return nil
	}
	if r.Dropped > 0 {
		u.restoreContentNotice(fmt.Sprintf("%s: %d earlier hook observations were not retained", info.ID, r.Dropped))
	}
	if store != nil && len(r.Runs) > 0 {
		if err := store.locked(u.session.waitContext, func(store *mekugiReplayStore) error {
			return store.retainFiles(history.HookObservationsName(u.session.cwd, info.ID))
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
