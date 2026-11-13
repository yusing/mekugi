package launch

import (
	json "encoding/json/v2"
	"errors"
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
