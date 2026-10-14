package router

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// Called only after confirmed graceful host shutdown. Host threads and their
// helpers must stop before removing scratch; uncertain shutdown retains it.
func (u *appServerUI) releaseWorkflowReviewStorage() error {
	if u.proxy == nil || u.proxy.orchestration == nil {
		return nil
	}
	// Discovery is not host ownership. Shared reviewer references retain the
	// confirmed client even when presentation or batch admission is discarded.
	var owned []workflowReviewerReset
	u.proxy.mu.RLock()
	for _, ref := range u.proxy.reviewerResets {
		if ref.host != nil && ref.host == u.client {
			owned = append(owned, *ref)
		}
	}
	u.proxy.mu.RUnlock()
	ctx := context.Background()
	var errs []error
	for _, ref := range owned {
		store := &orchestrate.Store{Directory: ref.run.Directory}
		errs = append(errs, store.ReleaseWorkflowReviewStorage(ctx, ref.run.Workspace, ref.run.Main, ref.batch, ref.dispatch, true))
	}
	return errors.Join(errs...)
}
