package launch

import (
	json "encoding/json/v2"
	"time"

	"github.com/yusing/mekugi/contracts/observation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func retainCompaction(retained history.Session, entry sessionpresentation.ActivityEntry) error {
	n := entry.Native
	if retained.Store == nil || n == nil || entry.Kind != "compaction" {
		return nil
	}
	r := history.CompactionObservation{Thread: n.Thread, Item: n.Item, Context: n.CompactionContext, Method: n.CompactionMethod}
	if n.CompactionTimed {
		r.ElapsedNS = new(int64(n.Duration))
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return retained.Store.Transaction(retained.Context, retained.Writes, func(tx *history.Transaction) error {
		return writeRetainedRecord(tx, retained.Thread, retained.Limits, history.CompactionName(n.Thread, n.Item), "compaction-pending-", data)
	})
}

func applyCompactionObservations(attached *attachedPresentation, restoring *appServerActivityRestore, read func(string, string) (*history.CompactionObservation, int, error)) {
	for _, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		for i, record := range source.Entries {
			entry := record.Source
			n := entry.Native
			if entry.Kind != "compaction" || n == nil {
				continue
			}
			r, _, err := read(n.Thread, n.Item)
			if err != nil {
				restoreHistoryNotice(attached, restoring, "Compaction measurements unavailable: "+err.Error(), true)
				continue
			}
			if r != nil {
				n.CompactionMethod = r.Method
				if r.Method == observation.ContextReset {
					n.SummaryPending = false
					entry.Text = ""
				}
				n.CompactionContext = r.Context
				if r.ElapsedNS != nil {
					n.Duration, n.CompactionTimed = time.Duration(*r.ElapsedNS), true
				}
				source.Model.Replace(i, entry, sessionpresentation.ActivityContent(entry).Values())
			}
		}
	}
}
