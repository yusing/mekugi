package router

import (
	"context"
	"errors"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// savedDiff hydrates admitted retained evidence for Diff and Activity consumers.
// It owns no terminal interaction, renderer or capture lifecycle.
// Source: apps/mekugi/internal/router/live_diff_terminal.go:315:378@[fc0514ff]
type savedDiff struct {
	store          *mekugiReplayStore
	data           *liveDiffData
	scope          previewmodel.Scope
	coverage       string
	awaitingResync bool
}

func (c *savedDiff) reset() {
	c.data, c.scope, c.coverage = newLiveDiffData(), previewmodel.Scope{}, "CONNECTING"
	c.awaitingResync = true
}

func (c *savedDiff) applyEvent(ctx context.Context, event reviewEvent) (bool, error) {
	if err := validateLiveDiffEvent(event); err != nil {
		return false, err
	}
	if c.awaitingResync {
		if event.Kind != "scope" || !event.Resync {
			return false, nil
		}
		c.awaitingResync = false
	}
	switch event.Kind {
	case "coverage":
		c.coverage = event.Status
	case "scope":
		if event.Resync {
			next, err := c.store.liveDiffSnapshot(ctx, *event.Scope)
			if err != nil {
				return false, err
			}
			for key := range c.data.attempts {
				if _, exists := next.attempts[key]; !exists {
					return false, errors.New("change records were removed; restart the live view")
				}
			}
			c.data = next
		} else if err := c.data.reconcile(ctx, c.store, *event.Scope); err != nil {
			return false, err
		}
		c.scope, c.coverage = *event.Scope, event.Status
	case "change":
		for _, change := range event.Changes {
			if !c.scope.Workspaces[change.Workspace][change.Thread] {
				continue
			}
			if err := c.data.apply(ctx, c.store, change); err != nil {
				return false, err
			}
		}
	}
	return event.Kind == "scope" || event.Kind == "change" || event.Kind == "coverage", nil
}
