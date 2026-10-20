package executionobservation

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/apps/mekugi/internal/recordtransactions"
	"github.com/yusing/mekugi/libraries/legacycall"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func publishObservations(ctx context.Context, selected changes.Scope, source changes.Service, publish func([]changes.Update), workspace string, histories map[string]mekugiHistory) error {
	if selected.Store == nil {
		return nil
	}
	selected = observationScope(ctx, selected)
	updates, err := recordtransactions.PublishEvidence(ctx, selected, source, workspace, changePublications(histories), func(tx *history.Transaction) error {
		for _, id := range slices.SortedFunc(maps.Keys(histories), func(a, b string) int {
			return cmp.Or(cmp.Compare(histories[a].sequence, histories[b].sequence), strings.Compare(a, b))
		}) {
			if err := publishObservation(tx, selected, legacycall.Record{Version: 1, Workspace: workspace, CallID: id, History: retainedHistory(histories[id])}); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		notifyChanges(publish, updates)
	}
	return changeError(err)
}

// The application classifies capture eligibility; Session History owns merging,
// ordering and publication of the neutral retained envelope.
func publishObservation(tx *history.Transaction, selected changes.Scope, record legacycall.Record) error {
	failed := record.History.ExecOutcome != nil && record.History.ExecOutcome.Status == string(changecapture.StatusFailed) && record.History.ExecOutcome.SharedWith == ""
	ordered := record.History.ChangeID != "" && len(record.History.ReviewFiles) > 0 || failed
	err := tx.PublishHostCall(selected.Thread, selected.Limits, record, ordered)
	var publication *history.PublicationError
	var conflict *history.HostConflict
	var capacity *history.CapacityError
	if errors.As(err, &publication) || errors.As(err, &conflict) || errors.As(err, &capacity) {
		return requestexecution.StoragePublicationError(err)
	}
	return err
}
