package sessionreview

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

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// hydrateReview admits retained host evidence into one unpublished module job.
// Backend/history interpretation and filesystem reads remain in this adapter.
// Source: apps/mekugi/internal/router/live_diff_data.go:24:107@[bab31cbe] retained evidence.
func hydrateReview(ctx context.Context, retained changes.Scope, builder *presentation.Builder, event Change) error {
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
		record, found, err := retained.Store.ReadHostCall(event.Workspace, call, false)
		if err != nil {
			return err
		}
		if !found || record.History.ChangeID != event.ID || record.History.CorrelationID != event.Correlation {
			return fmt.Errorf("change %s has a missing or inconsistent attempt", event.ID)
		}
		history := record.History
		files := make([]diffmodel.ReviewFile, len(history.ReviewFiles))
		for i, file := range history.ReviewFiles {
			files[i] = diffmodel.ReviewFile(file)
		}
		files, _ = changes.AuthoredEvidence(files, "")
		item, _, _ := strings.Cut(history.CorrelationID, "\x00")
		attempt := presentation.Attempt{AttemptIdentity: identity, Operation: activitydetail.Operation{Thread: cmp.Or(history.ExecutingThread, event.Thread), Item: item}}
		if history.ExecOutcome != nil && history.ExecutingThread != "" {
			attempt.Receipt = capturedReceipt(event.Workspace, history, files)
		}
		origin := presentation.Origin{Change: event.ID, Caller: history.Caller, Source: cmp.Or(history.Source, history.ToolName)}
		for n, file := range files {
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
func reconcileReview(ctx context.Context, retained changes.Scope, service changes.Service, builder *presentation.Builder, scope previewmodel.Scope) error {
	present := make(map[presentation.AttemptKey]bool)
	for _, workspace := range slices.Sorted(maps.Keys(scope.Workspaces)) {
		if !filepath.IsAbs(workspace) {
			return errors.New("live diff workspace must be absolute")
		}
		indexes, err := service.InspectVisible(retained, workspace, scope.Workspaces[workspace])
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
					if err := hydrateReview(ctx, retained, builder, FromUpdate(changes.Update{
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

// Completed host identity is correlated before producing feature presentation.
func capturedReceipt(workspace string, value legacycall.History, files []diffmodel.ReviewFile) *presentation.Receipt {
	if value.ExecOutcome == nil {
		return nil
	}
	evidence := presentation.ReceiptEvidence{Files: files, Source: binding.EditReceiptSource(value.ToolName, value.Source), ChangeID: value.ChangeID, Labels: value.ExecOutcome.Labels, DeletedDirectories: value.ExecOutcome.DeletedDirectories}
	receipt := &presentation.Receipt{Thread: value.ExecutingThread, Text: presentation.ReceiptText(workspace, evidence)}
	if !value.ExecOutcome.CodeMode {
		if call, ok := strings.CutSuffix(value.CorrelationID, "\x00exec"); ok {
			receipt.Calls = append(receipt.Calls, call)
		}
	}
	for _, result := range value.HostResults {
		if result.Tool == "exec_command" || result.Tool == "userShell" {
			receipt.Calls = append(receipt.Calls, result.CallID)
		}
	}
	if receipt.Text == "" && len(receipt.Calls) == 0 {
		return nil
	}
	return receipt
}
