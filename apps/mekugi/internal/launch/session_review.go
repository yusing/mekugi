package launch

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/contracts/activitydetail"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func attachReview(ctx context.Context, review *sessionreview.Review, retained changes.Scope, source changes.Service) *sessionreview.Review {
	if review == nil {
		review = sessionreview.New(ctx, nil, nil)
	}
	if retained.Store != nil {
		review.Projection.Changes, review.Projection.Retained = source, retained
	}
	return review
}

// Source: apps/mekugi/internal/router/native_shell.go:61:243@51f24bbd.
// preview joins completed receipts independently of terminal card lifetime.
func applyPreview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, pending *[]*changepresentation.Receipt, at time.Time, preview previewmodel.Preview, restored bool) {
	if preview.Complete && len(preview.HostItems) > 0 {
		receipt := &changepresentation.Receipt{Thread: preview.Thread, Calls: preview.HostItems, Text: preview.EditReceipt}
		for _, file := range preview.Files {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			receipt.Pages = append(receipt.Pages, activitydetail.Page{ID: preview.ID + "/" + strconv.Itoa(len(receipt.Pages)), Verb: file.Action().Title(),
				Path: pathdisplay.ForWorkspace(preview.Workspace, path), Code: file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(preview.Workspace, file.BeforePath), pathdisplay.ForWorkspace(preview.Workspace, file.AfterPath)), Language: "diff"})
			if file.Action().Title() == "Move" {
				receipt.Pages[len(receipt.Pages)-1].DisplayPath = pathdisplay.Move(preview.Workspace, file.BeforePath, file.AfterPath)
			}
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
		*pending = append(*pending, receipt)
		applyCapturedEdits(attached, review, pending)
		// Host rows can lag several preview mailbox drains. Bound unmatched
		// projections by memory, not one mailbox's count. Charge metadata too,
		// so tiny receipts cannot grow without a bound. These are not durable
		// changes or evidence of the whole command's success.
		budget := 32 * (48 << 10)
		for i := range slices.Backward(*pending) {
			entry := (*pending)[i]
			budget -= 1024 + len(entry.Thread) + len(entry.Text)
			for _, call := range entry.Calls {
				budget -= 32 + len(call)
			}
			for _, page := range entry.Pages {
				budget -= 1024 + len(page.Path) + len(page.Code)
			}
			if budget < 0 {
				*pending = slices.Delete(*pending, 0, i+1)
				break
			}
		}
	}
	if terminal != nil {
		terminal.Live.Observe(preview, restored, at)
	}
}

// Reject a retired subscription before its queued scope can authorize any read.
func admitReviewEvents(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), pending *[]*changepresentation.Receipt, clock func() time.Time, ctx context.Context, sub *sessionreview.Subscription, events ...sessionreview.Event) bool {
	if sub.Invalidated() {
		return false
	}
	for _, event := range events {
		applyReview(attached, terminal, review, source, prepare, pending, clock, ctx, event)
	}
	return review.Ready()
}

func applyReview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), pending *[]*changepresentation.Receipt, clock func() time.Time, ctx context.Context, event sessionreview.Event) {
	admitted, published := review.Apply(ctx, event)
	if !admitted {
		return
	}
	if event.Kind == "preview" {
		applyPreview(attached, terminal, review, pending, presentationTime(clock), *event.Preview, event.Resync)
		return
	}
	if terminal != nil {
		switch event.Kind {
		case "scope":
			if event.Resync && event.Scope != nil {
				terminal.Live.Resync(*event.Scope, event.PreviewIDs)
			}
		case "change":
			terminal.DiffUnseen = terminal.DiffUnseen || !terminal.DiffOpen
		}
	}
	if published {
		publishReview(attached, terminal, review, source, prepare)
	}
	if review.Loader == nil && (event.Kind == "scope" || event.Kind == "change") {
		applyCapturedEdits(attached, review, pending)
	}
}

func loadedReview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), pending *[]*changepresentation.Receipt, result sessionreview.Result) {
	if review.Loaded(result) {
		publishReview(attached, terminal, review, source, prepare)
		if review.Failure == "" {
			applyCapturedEdits(attached, review, pending)
		}
	}
}

func publishReview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store)) {
	projection := &review.Projection
	snapshot := projection.Data
	source.PublishReview(attached.session.Scope(), snapshot, projection.Workspace, projection.Coverage, review.Failure)
	if terminal != nil && prepare != nil {
		prepare(attached.session.Scope())
	}
	if attached.childSource != nil {
		attached.childSource.Model.LineCounts, attached.childSource.Model.NetCounts = snapshot.Publication.Counts.Callers, snapshot.Publication.Counts.Net
	}
}
