package router

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// This follows retained terminal evidence, never an interrupt acknowledgement.
// Archive owns Codex runtime teardown; views, accounting and receipts stay intact.
func (u *appServerUI) archiveWorkflowReviewer(work *orchestrateReviewWork) {
	d, child := work.dispatch, work.child
	if d.Kind != orchestrate.DispatchCorrectness && d.Kind != orchestrate.DispatchSimplification {
		return
	}
	c := child.command
	dispatch := false
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		var err error
		dispatch, err = s.BeginReviewerArchive(ctx, c.workspace, c.main, child.batch.TaskName, d.ID)
		if err != nil || !dispatch {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if usage := u.proxy.usage; usage != nil {
			if err := usage.flush(ctx); err != nil {
				return err
			}
			usage.mu.Lock()
			failed := usage.storageFailed[d.ThreadID]
			usage.mu.Unlock()
			if failed {
				return errors.New("reviewer usage is not durably retained")
			}
		}
		// Codex also archives spawned descendants. Unsettled descendants must
		// remain available, even when their reviewer root has finished.
		journals, failures, err := u.proxy.journals.relatedJournals(u.proxy.replayStore.scoped(ctx), child.batch.Cwd, d.ThreadID)
		if err != nil {
			return err
		}
		for thread, journal := range journals {
			if thread != d.ThreadID && slices.Contains(journalAncestry(journals, thread), d.ThreadID) && (failures[thread] != nil || journal.LifecycleState != "done") {
				return errors.New("reviewer archive requires settled native descendants")
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			if dispatch {
				u.recordWorkflowReviewerArchive(work, false, err)
			} else {
				u.setNotice("Reviewer archive: "+err.Error(), true)
			}
			return
		}
		if !dispatch {
			return
		}
		if u.orchestrateClosing || u.ctx.Err() != nil {
			u.recordWorkflowReviewerArchive(work, false, context.Canceled)
			return
		}
		if n := u.navigation; n != nil {
			if v := n.views[d.ThreadID]; v != nil && (v.busy() || len(v.activeChildren) != 0 || v.questionCount() != 0 || len(v.approvals.pending) != 0) {
				u.recordWorkflowReviewerArchive(work, false, errors.New("reviewer has active host work"))
				return
			}
		}
		if err := u.orchestrateRPCRequest(orchestrateRPC{method: "thread/archive", child: child, review: work}, map[string]any{"threadId": d.ThreadID}); err != nil {
			u.recordWorkflowReviewerArchive(work, true, err)
		}
	})
}

func (u *appServerUI) recordWorkflowReviewerArchive(work *orchestrateReviewWork, dispatched bool, hostErr error) {
	c, child := work.child.command, work.child
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		if err := s.RecordReviewerArchive(ctx, c.workspace, c.main, child.batch.TaskName, work.dispatch.ID, dispatched, hostErr); err != nil {
			return err
		}
		if dispatched && hostErr == nil {
			return s.ReleaseWorkflowReviewStorage(ctx, c.workspace, c.main, child.batch.TaskName, work.dispatch.ID)
		}
		return nil
	}, func(err error) {
		if err = errors.Join(hostErr, err); err != nil {
			u.setNotice("Reviewer archive: "+err.Error()+"; inspect retained host history before recovery", true)
		}
	})
}
