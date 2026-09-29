package router

import (
	"context"
	"errors"
)

func (p *mekugiProxy) observeJournalHostTurn(ctx context.Context, workspace, method string, event appServerEvent) error {
	state, reason := "", ""
	if method == "turn/started" {
		state = "working"
	}
	if method == "turn/completed" {
		if event.Turn.Status == "completed" {
			state = "done"
		} else {
			state, reason = "blocked", "Host turn "+event.Turn.Status
		}
	}
	if state == "" || event.ThreadID == "" {
		return nil
	}
	// Requests without workspace metadata keep their journals in the unscoped
	// namespace, which the native frontend also presents. Only a namespace that
	// holds the child's record changes; the other is a no-op.
	err := p.journals.observeLifecycle(ctx, p.replayStore, workspace, event.ThreadID, state, reason)
	if workspace != "" {
		err = errors.Join(err, p.journals.observeLifecycle(ctx, p.replayStore, "", event.ThreadID, state, reason))
	}
	return err
}
