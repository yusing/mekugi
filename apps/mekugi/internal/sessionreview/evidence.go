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
	"github.com/yusing/mekugi/modules/changehistory/evidence"
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
		record, found, err := retained.Store.ReadAttachedHostCall(event.Workspace, call, false)
		if err != nil {
			return err
		}
		if !found || record.History.ChangeID != event.ID || record.History.CorrelationID != event.Correlation {
			return fmt.Errorf("change %s has a missing or inconsistent attempt", event.ID)
		}
		history := record.History
		source := &evidence.Source{Store: retained.Store}
		root, err := evidence.Root(record)
		if err != nil {
			return err
		}
		var reviews []evidence.Review
		var sourceAnchor activitydetail.SourceAnchor
		hasObservation, codeMode := history.ExecObservation != nil, false
		if hasObservation {
			codeMode = history.ExecObservation.CodeMode
		}
		if root == "" {
			reviews, err = evidence.Reviews(source, record)
		} else {
			manifest, readErr := evidence.Open(source, root)
			if readErr != nil {
				return readErr
			}
			sourceAnchor = manifest.SourceAnchor
			reviews, err = evidence.ReadReviews(source, manifest.Reviews)
			if err == nil {
				hasObservation, codeMode, err = manifest.ObservationMode()
			}
		}
		if err != nil {
			return err
		}
		files, err := evidence.LoadReviews(reviews, func(file diffmodel.ReviewFile) bool {
			return changes.AuthoredReview(file) || changes.GitDiagnosticReview(file)
		}, changes.MaxReadBytes)
		if err != nil {
			return err
		}
		item, _, _ := strings.Cut(history.CorrelationID, "\x00")
		attempt := presentation.Attempt{AttemptIdentity: identity, Operation: activitydetail.Operation{Thread: cmp.Or(history.ExecutingThread, event.Thread), Item: item}}
		if (history.ExecOutcome != nil || history.ToolName == "apply_patch" && len(history.HostResults) > 1) && history.ExecutingThread != "" {
			attempt.Receipt = CapturedReceipt(event.Workspace, history, files)
		} else if writer, interval := strings.CutSuffix(history.CorrelationID, "\x00exec"); interval && hasObservation {
			// A writer interval becomes visible only with its terminal membership.
			// Borrow that operation's real host identity without altering the
			// earlier immutable boundary or fabricating an intermediate completion.
			terminal, found, err := retained.Store.ReadAttachedHostCall(event.Workspace, legacycall.ExecCallID(writer, codeMode), false)
			if err != nil {
				return err
			}
			if found {
				attempt.Receipt = CapturedReceipt(event.Workspace, terminal.History, files)
				sourceAnchor, err = evidence.Anchor(source, terminal)
				if err != nil {
					return err
				}
			}
		}
		if attempt.Receipt != nil {
			attempt.Receipt.SourceAnchor = sourceAnchor
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
		for _, index := range indexes {
			if !filepath.IsAbs(index.Workspace) {
				return errors.New("live diff workspace must be absolute")
			}
			for stream, info := range index.Streams {
				for number := 1; number <= info.Next; number++ {
					id := changes.ID(index.Streams[stream].Name, number)
					change := index.Changes[id]
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

// CapturedReceipt correlates retained command identity for live annotations and
// restored review publications before producing feature presentation.
func CapturedReceipt(workspace string, value legacycall.History, files []diffmodel.ReviewFile) *presentation.Receipt {
	return capturedReceipt(workspace, value, presentation.ReceiptEvidence{Files: files})
}

// CapturedRetainedReceipt detaches a bounded excerpt and exact counts. It never
// expands a large captured batch just to publish its Activity receipt.
func CapturedRetainedReceipt(workspace string, value legacycall.History, reviews []evidence.Review) (*presentation.Receipt, error) {
	files, counts, truncated, err := evidence.ReceiptFiles(reviews, presentation.MaxReceiptDiffBytes)
	if err != nil {
		return nil, err
	}
	return capturedReceipt(workspace, value, presentation.ReceiptEvidence{Files: files, Counts: counts, Truncated: truncated}), nil
}

func capturedReceipt(workspace string, value legacycall.History, input presentation.ReceiptEvidence) *presentation.Receipt {
	if value.ExecOutcome == nil && value.ToolName != "apply_patch" {
		return nil
	}
	evidence := input
	evidence.Source, evidence.ChangeID = binding.EditReceiptSource(value.ToolName, value.Source), value.ChangeID
	if value.ExecOutcome != nil {
		evidence.Labels, evidence.DeletedDirectories = value.ExecOutcome.Labels, value.ExecOutcome.DeletedDirectories
	}
	receipt := presentation.NewReceipt(workspace, evidence)
	receipt.Thread = value.ExecutingThread
	for _, result := range value.HostResults {
		if result.Tool == "exec_command" || result.Tool == "userShell" || result.Tool != "" && result.Tool == value.ToolName {
			receipt.Calls = append(receipt.Calls, result.CallID)
		}
	}
	if value.ToolName == "apply_patch" && len(receipt.Calls) == 0 {
		return nil // Keep the retained operation identity when no host join was witnessed.
	}
	if len(receipt.Calls) == 0 && value.ExecOutcome != nil && !value.ExecOutcome.CodeMode {
		if call, ok := strings.CutSuffix(value.CorrelationID, "\x00exec"); ok {
			receipt.Calls = append(receipt.Calls, call)
		}
	}
	if receipt.Text == "" && len(receipt.Calls) == 0 {
		return nil
	}
	return receipt
}
