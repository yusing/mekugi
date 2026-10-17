package router

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/contracts/activitydetail"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func (u *appServerUI) attachReview() {
	if u.review == nil {
		u.review = sessionreview.New(u.ctx, nil, nil)
	}
	if u.retained.Store != nil {
		u.review.Projection.Changes, u.review.Projection.Retained = u.changeSource, u.retained
	}
}

// Source: apps/mekugi/internal/router/native_shell.go:61:243@51f24bbd.
// preview joins completed receipts independently of terminal card lifetime.
func (u *appServerUI) preview(preview previewmodel.Preview, restored bool) {
	if preview.Complete && len(preview.HostItems) > 0 {
		receipt := &changepresentation.Receipt{Thread: preview.Thread, Calls: preview.HostItems, Text: preview.EditReceipt}
		for _, file := range preview.Files {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			receipt.Pages = append(receipt.Pages, activitydetail.Page{ID: preview.ID + "/" + strconv.Itoa(len(receipt.Pages)), Verb: file.Action().Title(),
				Path: pathdisplay.ForWorkspace(preview.Workspace, path), Code: file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(preview.Workspace, file.BeforePath), pathdisplay.ForWorkspace(preview.Workspace, file.AfterPath)), Language: "diff"})
		}
		if len(receipt.Pages) == 0 {
			// Large live cards discard Files. Their receipt still carries bounded
			// observed hunks, including an explicit marker for omitted lines.
			for _, paragraph := range activitymodel.Paragraphs(receipt.Text) {
				block := activitypresentation.Value{Content: activitymodel.ParseOperation(paragraph)}
				if block.Lang == "diff" && block.Code != "" {
					block.Path = block.EditPath()
					receipt.Pages = append(receipt.Pages, activitydetail.Page{ID: preview.ID + "/" + strconv.Itoa(len(receipt.Pages)), Verb: block.Verb, Path: block.Path, Code: block.Code, Language: block.Lang})
				}
			}
		}
		u.pendingEditReceipts = append(u.pendingEditReceipts, receipt)
		u.applyCapturedEdits()
		// Host rows can lag several preview mailbox drains. Bound unmatched
		// projections by memory, not one mailbox's count. Charge metadata too,
		// so tiny receipts cannot grow without a bound. These are not durable
		// changes or evidence of the whole command's success.
		budget := 32 * (48 << 10)
		for i := len(u.pendingEditReceipts) - 1; i >= 0; i-- {
			pending := u.pendingEditReceipts[i]
			budget -= 1024 + len(pending.Thread) + len(pending.Text)
			for _, call := range pending.Calls {
				budget -= 32 + len(call)
			}
			for _, page := range pending.Pages {
				budget -= 1024 + len(page.Path) + len(page.Code)
			}
			if budget < 0 {
				u.pendingEditReceipts = slices.Delete(u.pendingEditReceipts, 0, i+1)
				break
			}
		}
	}
	if u.shell != nil {
		u.shell.liveDock.Observe(preview, restored, u.now())
	}
}

// Reject a retired subscription before its queued scope can authorize any read.
func (u *appServerUI) admitReviewEvents(ctx context.Context, sub *sessionreview.Subscription, events ...sessionreview.Event) bool {
	if sub.Invalidated() {
		return false
	}
	for _, event := range events {
		u.applyReview(ctx, event)
	}
	return u.review.Ready()
}

func (u *appServerUI) applyReview(ctx context.Context, event sessionreview.Event) {
	admitted, published := u.review.Apply(ctx, event)
	if !admitted {
		return
	}
	if event.Kind == "preview" {
		u.preview(*event.Preview, event.Resync)
		return
	}
	if u.shell != nil {
		switch event.Kind {
		case "scope":
			if event.Resync && event.Scope != nil {
				u.shell.liveDock.Resync(*event.Scope, event.PreviewIDs)
			}
		case "coverage":
			if strings.HasPrefix(event.Status, "RECONNECTING:") {
				u.shell.liveDock = previewview.PreviewPane{}
			}
		case "change":
			u.shell.diffUnseen = u.shell.diffUnseen || !u.shell.DiffOpen
		}
	}
	if published {
		u.publishReview()
	}
	if u.review.Loader == nil && (event.Kind == "scope" || event.Kind == "change") {
		u.applyCapturedEdits()
	}
}

func (u *appServerUI) loadedReview(result sessionreview.Result) {
	if u.review.Loaded(result) {
		u.publishReview()
		if u.review.Failure == "" {
			u.applyCapturedEdits()
		}
	}
}

func (u *appServerUI) publishReview() {
	projection := &u.review.Projection
	snapshot := projection.Data
	u.reviewSource.PublishReview(u.session.Scope(), snapshot, projection.Workspace, projection.Coverage, u.review.Failure)
	if u.shell != nil && u.shell.preparePane != nil {
		u.shell.preparePane(u.session.Scope())
	}
	if u.childSource != nil {
		u.childSource.Model.LineCounts, u.childSource.Model.NetCounts = snapshot.Publication.Counts.Callers, snapshot.Publication.Counts.Net
	}
}
