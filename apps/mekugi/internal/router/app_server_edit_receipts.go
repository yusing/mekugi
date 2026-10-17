package router

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func applyCapturedEdits(attached *attachedPresentation, review *sessionreview.Review, pending *[]*changepresentation.Receipt) {
	*pending = slices.DeleteFunc(*pending, func(receipt *changepresentation.Receipt) bool {
		// Main and Activity receive their host entries together. Keep a grouped
		// receipt until every host identity has arrived, including its anchor.
		attached.source.ApplyCapturedEdits([]*changepresentation.Receipt{receipt})
		attached.childSource.ApplyCapturedEdits([]*changepresentation.Receipt{receipt})
		return !slices.ContainsFunc(receipt.Calls, func(call string) bool {
			return !slices.ContainsFunc(attached.childSource.Entries, func(entry sessionpresentation.ActivityRecord) bool {
				return entry.Source.Native != nil && entry.Source.Native.Thread == receipt.Thread && entry.Source.Native.Item == call
			})
		})
	})
	if review == nil || review.Projection.Data == nil {
		return
	}
	attached.source.ApplyCapturedEdits(review.Projection.Data.Receipts())
	attached.childSource.ApplyCapturedEdits(review.Projection.Data.Receipts())
}
