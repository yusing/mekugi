package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (u *appServerUI) retainHook(thread string, observation history.HookObservation) error {
	store := u.retainedSession.Store // Existing native-session managed store and lease.
	if store == nil {
		return errors.New("native session store is unavailable")
	}
	return store.Transaction(u.retainedSession.Context, u.retainedSession.Writes, func(tx *history.Transaction) error {
		r, err := store.ReadHookObservations(u.cwd, thread)
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
			if len(data) <= history.MaxRecordBytes {
				return writeRetainedRecord(tx, u.retainedSession.Thread, u.retainedSession.Limits, history.HookObservationsName(u.cwd, thread), "hooks-pending-", data)
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

func (u *appServerUI) restoredHookEvents(info binding.ThreadInfo) []restoredRolloutEvent {
	store := u.retainedSession.Store
	r, err := store.ReadHookObservations(u.cwd, info.ID)
	if err != nil {
		u.restoreContentNotice("Hook observations for " + info.ID + " unavailable: " + err.Error())
		return nil
	}
	if r.Dropped > 0 {
		u.restoreContentNotice(fmt.Sprintf("%s: %d earlier hook observations were not retained", info.ID, r.Dropped))
	}
	if store != nil && len(r.Runs) > 0 {
		if err := store.Transaction(u.retainedSession.Context, u.retainedSession.Writes, func(tx *history.Transaction) error {
			return requestexecution.StoragePublicationError(tx.Retain(u.retainedSession.Thread, u.retainedSession.Limits, history.HookObservationsName(u.cwd, info.ID)))
		}); err != nil {
			u.restoreContentNotice("Hook observations for " + info.ID + " could not be retained: " + err.Error())
		}
	}
	var events []restoredRolloutEvent
	for _, observation := range r.Runs {
		entry := u.hookEntry(info.ID, observation, false)
		// Position by time, including thread-scoped hooks outside any turn.
		events = append(events, restoredRolloutEvent{at: observation.At, entry: entry})
	}
	return events
}
