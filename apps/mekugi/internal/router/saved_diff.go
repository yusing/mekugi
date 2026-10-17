package router

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// savedDiff hydrates admitted retained evidence for Diff and Activity consumers.
// It owns no terminal interaction, renderer or capture lifecycle.
// Source: apps/mekugi/internal/router/live_diff_terminal.go:315:378@[fc0514ff]
type savedDiff struct {
	store          *mekugiReplayStore
	data           *presentation.Snapshot
	scope          previewmodel.Scope
	coverage       string
	awaitingResync bool
}

func (c *savedDiff) reset() {
	c.data, c.scope, c.coverage = new(presentation.Snapshot), previewmodel.Scope{}, "CONNECTING"
	c.awaitingResync = true
}

func (c *savedDiff) applyEvent(ctx context.Context, events ...reviewEvent) (bool, error) {
	next := *c
	builder := presentation.NewBuilder(c.data)
	changed, content := false, false
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := validateLiveDiffEvent(event); err != nil {
			return false, err
		}
		if next.awaitingResync {
			if event.Kind != "scope" || !event.Resync {
				continue
			}
			next.awaitingResync = false
		}
		switch event.Kind {
		case "coverage":
			next.coverage, changed = event.Status, true
		case "scope":
			if next.store != nil {
				if err := reconcileReview(ctx, next.store, builder, *event.Scope); err != nil {
					return false, err
				}
			}
			next.scope, next.coverage = *event.Scope, event.Status
			changed, content = true, true
		case "change":
			for _, change := range event.Changes {
				if !next.scope.Workspaces[change.Workspace][change.Thread] {
					continue
				}
				if next.store == nil {
					return false, errors.New("saved change evidence is unavailable")
				}
				if err := hydrateReview(ctx, next.store, builder, change); err != nil {
					return false, err
				}
			}
			changed, content = true, true
		}
	}
	if content {
		next.data = builder.Finish()
	}
	*c = next
	return changed, nil
}
