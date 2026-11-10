package executionobservation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	sessionhistory "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/backends/codex/binding"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
)

// publishEditReceipt reads committed filesystem changes. It never accepts a
// caller-supplied diff or participates in edit execution. Command exit status
// does not change the filesystem effects that this receipt describes.
func publishEditReceipt(ctx context.Context, selected changes.Scope, source changes.Service, workspace, thread, callID string, activity *sessioncontrol.Audience, displayID string) error {
	selected = observationScope(ctx, selected)
	return selected.Store.Transaction(ctx, selected.Writes, func(*sessionhistory.Transaction) error {
		if scope, found, err := selected.Store.ReadScope(thread); err != nil {
			return err
		} else if found {
			selected.Namespace = scope.Namespace
		}
		record, found, err := selected.Store.ReadAttachedHostCall(workspace, callID, false)
		if err != nil {
			return err
		}
		if !found || record.History.ToolName != applyPatchToolName && record.History.ExecOutcome == nil ||
			record.History.ExecutingThread != thread {
			return fmt.Errorf("edit receipt not found")
		}
		index, err := source.Inspect(selected, workspace)
		if err != nil {
			return err
		}
		id := record.History.ChangeID
		for _, call := range index.Changes[id].Calls {
			if call.ID != callID || call.Thread != thread {
				continue
			}
			history := legacyHistory(record.History)
			history.sourceAnchor, err = evidence.Anchor(captureSource(selected), record)
			if err != nil {
				return err
			}
			history.ReviewFiles = nil
			history.reviewFiles = []evidence.Review{}
			for _, member := range index.Changes[id].Calls {
				part, found, err := selected.Store.ReadAttachedHostCall(workspace, member.ID, false)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("edit receipt interval is unavailable")
				}
				reviews, err := evidence.Reviews(captureSource(selected), part)
				if err != nil {
					return err
				}
				history.reviewFiles = append(history.reviewFiles, reviews...)
			}
			receipt, err := sessionreview.CapturedRetainedReceipt(workspace, record.History, history.reviews())
			if err != nil {
				return err
			}
			if receipt != nil {
				receipt.SourceAnchor = history.sourceAnchor
			}
			if receipt == nil && history.ToolName == applyPatchToolName && displayID != "" {
				receipt = changepresentation.NewReceipt(workspace, receiptEvidence(history))
				receipt.Calls = []string{displayID}
			}
			if receipt != nil && receipt.Text != "" {
				activity.Collect(activitypresentation.Supplement{Thread: thread, Source: "edit-receipt\x00" + workspace + "\x00" + callID, Kind: activitypresentation.SupplementEditReceipt, Text: receipt.Text, Calls: receipt.Calls, SourceAnchor: receipt.SourceAnchor})
			}
			return nil
		}
		return fmt.Errorf("edit receipt does not belong to publishing thread")
	})
}

// agentEditNotice is projected only after observation records are durable. The
// original host result remains a separate content part and is retained verbatim.
func agentEditNotice(ctx context.Context, selected changes.Scope, source changes.Service, workspace, callID string, history mekugiHistory) (string, error) {
	selected = observationScope(ctx, selected)
	var calls []string
	var captureErr error
	for index := range history.NativePatches {
		calls = append(calls, legacycall.PatchCallID(callID, index))
	}
	if history.ExecObservation != nil {
		calls = append(calls, legacycall.ExecCallID(callID, history.ExecObservation.CodeMode))
	}
	if history.ResolvedBaseline != nil {
		// Dynamic nested calls are unknown in the immutable pre-cell carrier.
		// Find their durable derived attempts, not the disposable native trace.
		if err := selected.Store.Transaction(ctx, selected.Writes, func(*sessionhistory.Transaction) error {
			index, err := source.Inspect(selected, workspace)
			if err != nil {
				return err
			}
			for _, change := range index.Changes {
				if strings.HasPrefix(change.Correlation, callID+"\x00") {
					for _, call := range change.Calls {
						calls = append(calls, call.ID)
					}
				}
			}
			return nil
		}); err != nil {
			captureErr = errors.Join(captureErr, err)
		}
		slices.Sort(calls)
		calls = slices.Compact(calls)
	}
	var ids []string
	var warnings []string
	warned := make(map[string]bool)
	addWarnings := func(record mekugiHistory) {
		for _, file := range record.reviews() {
			if file.Incomplete != "" && !warned[file.Incomplete] {
				warned[file.Incomplete] = true
				warnings = append(warnings, file.Incomplete)
			}
		}
		if outcome := record.ExecOutcome; outcome != nil && outcome.Coverage == "partial" {
			reason := outcome.ScopeReason
			if reason == "" {
				reason = "partial file evidence"
			}
			if !warned[reason] {
				warned[reason] = true
				warnings = append(warnings, reason)
			}
		}
	}
	seen := make(map[string]bool)
	for _, derived := range calls {
		record, found, err := lookupObservation(ctx, selected, workspace, derived)
		if err != nil {
			captureErr = errors.Join(captureErr, err)
			continue
		}
		if !found {
			continue
		}
		if record.ExecOutcome != nil && record.ExecOutcome.SharedWith != "" {
			shared := record.ExecOutcome.SharedWith
			record, found, err = lookupObservation(ctx, selected, workspace, shared)
			if err != nil {
				captureErr = errors.Join(captureErr, err)
				continue
			}
			if !found {
				captureErr = errors.Join(captureErr, fmt.Errorf("shared capture %s unavailable", shared))
				continue
			}
		}
		authored := authoredChangeHistory(record)
		addWarnings(authored)
		if authored.ExecOutcome != nil || len(authored.reviews()) == 0 {
			intervals, err := readExecIntervals(ctx, selected, source, workspace, callID, history.ExecObservation != nil && history.ExecObservation.CodeMode)
			if err != nil {
				captureErr = errors.Join(captureErr, err)
			}
			for _, interval := range intervals {
				addWarnings(authoredChangeHistory(interval.before))
			}
			if err == nil && len(intervals) == 0 && len(authored.reviews()) == 0 {
				continue
			}
		}
		id := authored.ChangeID
		if !found || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	// Success needs only durable IDs, not a composed diff or per-file statistics.
	notice := strings.Join(ids, " ")
	if len(warnings) != 0 {
		warning := strings.Join(warnings, "; ")
		const prefix = "\nCapture incomplete: "
		const truncated = " [truncated; inspect mchanges --history]"
		limit := max(0, changepresentation.MaxReceiptDiffBytes-len(notice)-len(prefix)-len(truncated))
		if len(warning) > limit {
			warning = strings.ToValidUTF8(warning[:limit], "") + truncated
		}
		notice = strings.TrimSpace(notice + prefix + warning)
	}
	return notice, captureErr
}

// receiptEvidence translates observation facts into renderer-neutral receipt input.
func receiptEvidence(value mekugiHistory) changepresentation.ReceiptEvidence {
	files, counts, truncated, err := evidence.ReceiptFiles(value.reviews(), changepresentation.MaxReceiptDiffBytes)
	if err != nil {
		files = []diffmodel.ReviewFile{diffmodel.RenderIncompleteReviewFile("", "", err.Error())}
		counts = nil
		truncated = nil
	}
	evidence := changepresentation.ReceiptEvidence{Files: files, Counts: counts, Truncated: truncated, Source: value.Source, ChangeID: value.ChangeID}
	evidence.Source = binding.EditReceiptSource(value.ToolName, value.Source)
	if value.ExecOutcome != nil {
		evidence.Labels, evidence.DeletedDirectories = value.ExecOutcome.Labels, value.ExecOutcome.DeletedDirectories
	}
	return evidence
}
