// Source: apps/mekugi/internal/router/storage_retention.go@[d6415ec1].
package requestexecution

import (
	"errors"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// StoragePublicationError preserves retained host-conflict and capacity classifications.
func StoragePublicationError(err error) error {
	if conflict, ok := errors.AsType[*history.HostConflict](err); ok {
		if conflict.Field == "" {
			return Classify(err, "replay_translation_conflict", "a completed tool call conflicted with its retained replay translation", true)
		}
		return Classify(err, "replay_item_conflict", "a completed tool call changed a retained replay item field", true)
	}
	if capacity, ok := errors.AsType[*history.CapacityError](err); ok {
		return Classify(err, "storage_capacity", capacity.Error(), false)
	}
	return StorageIOError(err)
}

// StorageIOError classifies failed session persistence without retrying it.
func StorageIOError(err error) error {
	if err == nil {
		return nil
	}
	return Classify(err, "storage_io", "Mekugi could not persist session data; check available disk space and storage permissions before retrying", false)
}
