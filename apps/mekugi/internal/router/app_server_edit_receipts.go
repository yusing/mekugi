package router

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func (u *appServerUI) applyCapturedEdits() {
	u.pendingEditReceipts = slices.DeleteFunc(u.pendingEditReceipts, func(receipt *changepresentation.Receipt) bool {
		// Main and Activity receive their host entries together. Keep a grouped
		// receipt until every host identity has arrived, including its anchor.
		u.attached.source.ApplyCapturedEdits([]*changepresentation.Receipt{receipt})
		u.attached.childSource.ApplyCapturedEdits([]*changepresentation.Receipt{receipt})
		return !slices.ContainsFunc(receipt.Calls, func(call string) bool {
			return !slices.ContainsFunc(u.attached.childSource.Entries, func(entry sessionpresentation.ActivityRecord) bool {
				return entry.Source.Native != nil && entry.Source.Native.Thread == receipt.Thread && entry.Source.Native.Item == call
			})
		})
	})
	if u.review == nil || u.review.Projection.Data == nil {
		return
	}
	u.attached.source.ApplyCapturedEdits(u.review.Projection.Data.Receipts())
	u.attached.childSource.ApplyCapturedEdits(u.review.Projection.Data.Receipts())
}
