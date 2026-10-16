package router

import (
	"errors"

	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func (s *mekugiReplayStore) changeScope() changes.Scope {
	return changes.Scope{Store: s.publication, Namespace: s.session.Namespace, Thread: s.session.Thread, Limits: s.retentionLimits(), Writes: s.writes}
}
func changePublications(histories map[string]mekugiHistory) map[string]changes.Publication {
	result := make(map[string]changes.Publication, len(histories))
	for id, value := range histories {
		managed := len(value.ReviewFiles) != 0
		for _, file := range value.ReviewFiles {
			managed = managed && file.Origin != ""
		}
		result[id] = changes.Publication{ChangeID: value.ChangeID, CorrelationID: value.CorrelationID, ExecutingThread: value.ExecutingThread, Sequence: value.sequence, Managed: managed, Confirmed: value.confirmed}
	}
	return result
}
func changeError(err error) error {
	if publication, ok := errors.AsType[*changes.PublicationError](err); ok {
		return storagePublicationError(publication.Err)
	}
	return err
}
func (s *mekugiReplayStore) notifyChanges(updates []changes.Update) {
	if s.liveDiff == nil || len(updates) == 0 {
		return
	}
	values := make([]reviewChange, 0, len(updates))
	for _, update := range updates {
		values = append(values, retainedReviewChange(update))
	}
	s.liveDiff(values)
}

// retainedReviewChange projects identities consumed by the saved-review loader.
// Record encoding, sequence allocation and call metadata remain changehistory-owned.
func retainedReviewChange(update changes.Update) reviewChange {
	value := reviewChange{Workspace: update.Workspace, Namespace: update.Namespace, Thread: update.Thread, Stream: update.Stream, ID: update.ID, Correlation: update.Change.Correlation}
	for _, call := range update.Change.Calls {
		value.Calls = append(value.Calls, call.ID)
	}
	return value
}
