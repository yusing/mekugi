package executionobservation

import (
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func changePublications(histories map[string]mekugiHistory) map[string]changes.Publication {
	result := make(map[string]changes.Publication, len(histories))
	for id, value := range histories {
		managed := len(value.ReviewFiles) != 0
		for _, file := range value.ReviewFiles {
			managed = managed && file.Origin != ""
		}
		result[id] = changes.Publication{ChangeID: value.ChangeID, CorrelationID: value.CorrelationID, ExecutingThread: value.ExecutingThread, Sequence: value.sequence, Managed: managed, Confirmed: value.confirmed, NoEffect: value.ChangeID == "" && len(value.ReviewFiles) == 0 && value.ExecOutcome != nil && value.ExecOutcome.Coverage == capture.CoverageExact}
	}
	return result
}
func changeError(err error) error {
	if publication, ok := errors.AsType[*changes.PublicationError](err); ok {
		return requestexecution.StoragePublicationError(publication.Err)
	}
	return err
}
func notifyChanges(publish func([]changes.Update), updates []changes.Update) {
	if publish != nil && len(updates) != 0 {
		publish(updates)
	}
}
