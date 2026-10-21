package sessionreview

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

// RewindChange identifies one whole retained change. The mutation owner orders it.
type RewindChange struct {
	Workspace, ID string
}

// RewindChanges only reads supplied host sources and the admitted snapshot.
// The caller owns cancellation, publication and any subsequent host execution.
func RewindChanges(ctx context.Context, snapshot *presentation.Snapshot, root, turn string, sources []binding.ThreadInfo) ([]RewindChange, error) {
	var boundary time.Time
	times := make(map[[2]string]time.Time)
	ends := make(map[[2]string]time.Time)
	for _, source := range sources {
		if !filepath.IsAbs(source.Path) {
			return nil, fmt.Errorf("thread %s has no absolute history path", source.ID)
		}
		inspection, err := rollout.Inspect(ctx, source.Path, func(event rollout.InspectionEvent) error {
			if source.ID == root && event.TurnID == turn && (event.Kind == "task_started" || event.Kind == "turn_started") {
				boundary = event.At
			}
			if event.ItemID != "" {
				key := [2]string{source.ID, event.ItemID}
				if event.Kind == "item_started" {
					times[key] = event.At
				} else if event.PersistedTimes {
					times[key], ends[key] = event.Started, event.Completed
				} else {
					ends[key] = event.At
				}
			}
			return ctx.Err()
		})
		if err != nil {
			return nil, fmt.Errorf("thread %s: %w", source.ID, err)
		}
		if inspection.ThreadID != source.ID {
			return nil, fmt.Errorf("thread %s history identity mismatch", source.ID)
		}
		for _, call := range inspection.Calls {
			if call.HasCall {
				at, _ := time.Parse(time.RFC3339Nano, call.Timestamp)
				times[[2]string{source.ID, call.Item.CallID}] = at
				ends[[2]string{source.ID, call.Item.CallID}], _ = time.Parse(time.RFC3339Nano, call.CompletedTimestamp)
			}
		}
	}
	if boundary.IsZero() {
		return nil, errors.New("selected turn has no recorded start time")
	}
	sides := make(map[[2]string]bool)
	for attempt := range snapshot.Attempts() {
		if len(attempt.Chunks) == 0 {
			continue
		}
		if !filepath.IsAbs(attempt.Key.Workspace) || attempt.Change == "" {
			return nil, errors.New("captured change has no workspace or identity")
		}
		at := times[[2]string{attempt.Operation.Thread, attempt.Operation.Item}]
		end := ends[[2]string{attempt.Operation.Thread, attempt.Operation.Item}]
		if !at.IsZero() && at.Before(boundary) && (end.IsZero() || !end.Before(boundary)) {
			return nil, fmt.Errorf("change %s has an uncertain execution boundary", attempt.Change)
		}
		if attempt.Receipt != nil {
			for _, call := range attempt.Receipt.Calls {
				candidate := times[[2]string{attempt.Receipt.Thread, call}]
				completed := ends[[2]string{attempt.Receipt.Thread, call}]
				if candidate.Before(boundary) && (completed.IsZero() || !completed.Before(boundary)) {
					return nil, fmt.Errorf("change %s has an uncertain execution boundary", attempt.Change)
				}
				if candidate.IsZero() {
					return nil, fmt.Errorf("change %s has no recorded call time", attempt.Change)
				}
				if !at.IsZero() && at.Before(boundary) != candidate.Before(boundary) {
					return nil, fmt.Errorf("change %s spans the selected turn boundary", attempt.Change)
				}
				at = candidate
			}
		}
		if at.IsZero() {
			return nil, fmt.Errorf("change %s has no recorded call time", attempt.Change)
		}
		key := [2]string{attempt.Key.Workspace, attempt.Change}
		after := !at.Before(boundary)
		if side, seen := sides[key]; seen && side != after {
			return nil, fmt.Errorf("change %s spans the selected turn boundary", attempt.Change)
		}
		sides[key] = after
	}
	var selected []RewindChange
	paths := make(map[string]string)
	for attempt := range snapshot.Attempts() {
		if !sides[[2]string{attempt.Key.Workspace, attempt.Change}] {
			continue
		}
		for _, chunk := range attempt.Chunks {
			for _, path := range []string{chunk.Review.BeforePath, chunk.Review.AfterPath} {
				if path == "" {
					continue
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(attempt.Key.Workspace, path)
				}
				path = filepath.Clean(path)
				if workspace, seen := paths[path]; seen && workspace != attempt.Key.Workspace {
					return nil, errors.New("selected changes overlap across workspace histories")
				}
				paths[path] = attempt.Key.Workspace
			}
		}
	}
	for key, after := range sides {
		if after {
			selected = append(selected, RewindChange{Workspace: key[0], ID: key[1]})
		}
	}
	slices.SortFunc(selected, func(a, b RewindChange) int {
		return cmp.Or(cmp.Compare(a.Workspace, b.Workspace), cmp.Compare(a.ID, b.ID))
	})
	return selected, ctx.Err()
}
