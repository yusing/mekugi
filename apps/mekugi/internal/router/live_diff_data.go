package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// hydrateReview admits retained host evidence into one unpublished module job.
// Backend/history interpretation and filesystem reads remain in this adapter.
func hydrateReview(ctx context.Context, store *mekugiReplayStore, builder *presentation.Builder, event sessionreview.Change) error {
	for _, call := range event.Calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity := presentation.AttemptIdentity{Key: presentation.AttemptKey{Workspace: event.Workspace, Call: call}, Change: event.ID, Correlation: event.Correlation, Stream: event.Stream}
		if found, err := builder.Known(identity); err != nil {
			return err
		} else if found {
			continue
		}
		record, found, err := store.read(event.Workspace, call, false)
		if err != nil {
			return err
		}
		if !found || record.History.ChangeID != event.ID || record.History.CorrelationID != event.Correlation {
			return fmt.Errorf("change %s has a missing or inconsistent attempt", event.ID)
		}
		history := authoredChangeHistory(record.History)
		item, _, _ := strings.Cut(history.CorrelationID, "\x00")
		attempt := presentation.Attempt{AttemptIdentity: identity, Operation: activitydetail.Operation{Thread: cmp.Or(history.ExecutingThread, event.Thread), Item: item}}
		if history.ExecOutcome != nil && history.ExecutingThread != "" {
			attempt.Receipt = capturedEditActivity(event.Workspace, history)
		}
		origin := presentation.Origin{Change: event.ID, Caller: history.Caller, Source: cmp.Or(history.Source, history.ToolName)}
		for n, file := range history.ReviewFiles {
			canonical := func(path string) string {
				if path == "" {
					return ""
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(event.Workspace, path)
				}
				return filepath.Clean(path)
			}
			file.BeforePath, file.AfterPath = canonical(file.BeforePath), canonical(file.AfterPath)
			attempt.Chunks = append(attempt.Chunks, presentation.Chunk{
				Key: event.Workspace + "\x00" + call + "/" + strconv.Itoa(n), Stream: event.Workspace + "\x00" + event.Namespace + "\x00" + strconv.Itoa(event.Stream),
				Workspace: event.Workspace, CaptureOrder: record.CaptureOrder, Review: file, Origin: origin,
			})
		}
		if err := builder.Add(attempt); err != nil {
			return err
		}
	}
	return nil
}

// Reconciliation reuses admitted immutable attempts, while checking current
// scope membership before a complete replacement can be published.
func reconcileReview(ctx context.Context, store *mekugiReplayStore, builder *presentation.Builder, scope previewmodel.Scope) error {
	present := make(map[presentation.AttemptKey]bool)
	for _, workspace := range slices.Sorted(maps.Keys(scope.Workspaces)) {
		if !filepath.IsAbs(workspace) {
			return errors.New("live diff workspace must be absolute")
		}
		indexes, err := store.changes.InspectVisible(store.changeScope(), workspace, scope.Workspaces[workspace])
		if err != nil {
			return err
		}
		threads := scope.Workspaces[workspace]
		for _, index := range indexes {
			for stream, info := range index.Streams {
				for number := 1; number <= info.Next; number++ {
					id := changes.ID(index.Streams[stream].Name, number)
					change := index.Changes[id]
					change.Calls = slices.Clone(change.Calls)
					change.Calls = slices.DeleteFunc(change.Calls, func(call changes.Call) bool {
						return threads != nil && !threads[cmp.Or(call.Thread, info.Thread)]
					})
					for _, call := range change.Calls {
						present[presentation.AttemptKey{Workspace: index.Workspace, Call: call.ID}] = true
					}
					if err := hydrateReview(ctx, store, builder, sessionreview.FromUpdate(changes.Update{
						Workspace: index.Workspace, Namespace: index.Namespace, Thread: info.Thread, Stream: stream, ID: id, Change: change,
					})); err != nil {
						return err
					}
				}
			}
		}
	}
	return builder.RequirePresent(present)
}
