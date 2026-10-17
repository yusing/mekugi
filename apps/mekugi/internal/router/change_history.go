package router

import (
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

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
		return requestexecution.StoragePublicationError(publication.Err)
	}
	return err
}
func (s *mekugiReplayStore) notifyChanges(updates []changes.Update) {
	if s.liveDiff != nil && len(updates) != 0 {
		s.liveDiff(updates)
	}
}
