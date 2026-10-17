package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func retainHook(retained history.Session, workspace string, thread string, observation history.HookObservation) error {
	store := retained.Store // Existing native-session managed store and lease.
	if store == nil {
		return errors.New("native session store is unavailable")
	}
	return store.Transaction(retained.Context, retained.Writes, func(tx *history.Transaction) error {
		r, err := store.ReadHookObservations(workspace, thread)
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
				return writeRetainedRecord(tx, retained.Thread, retained.Limits, history.HookObservationsName(workspace, thread), "hooks-pending-", data)
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

func restoredHookEvents(attached *attachedPresentation, activity *activitymodel.Service, retainedSession history.Session, restoring *appServerActivityRestore, info binding.ThreadInfo) []restoredRolloutEvent {
	store := retainedSession.Store
	r, err := store.ReadHookObservations(attached.cwd, info.ID)
	if err != nil {
		restoreHistoryNotice(attached, restoring, "Hook observations for "+info.ID+" unavailable: "+err.Error(), true)
		return nil
	}
	if r.Dropped > 0 {
		restoreHistoryNotice(attached, restoring, fmt.Sprintf("%s: %d earlier hook observations were not retained", info.ID, r.Dropped), true)
	}
	if store != nil && len(r.Runs) > 0 {
		if err := store.Transaction(retainedSession.Context, retainedSession.Writes, func(tx *history.Transaction) error {
			return requestexecution.StoragePublicationError(tx.Retain(retainedSession.Thread, retainedSession.Limits, history.HookObservationsName(attached.cwd, info.ID)))
		}); err != nil {
			restoreHistoryNotice(attached, restoring, "Hook observations for "+info.ID+" could not be retained: "+err.Error(), true)
		}
	}
	var events []restoredRolloutEvent
	for _, observation := range r.Runs {
		entry := hookEntry(&attached.session, activity, attached.cwd, info.ID, observation, false)
		// Position by time, including thread-scoped hooks outside any turn.
		events = append(events, restoredRolloutEvent{at: observation.At, entry: entry})
	}
	return events
}
