package executionobservation

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sessionhistory "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/backends/codex/binding"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
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
		record, found, err := selected.Store.ReadHostCall(workspace, callID, false)
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
			history.ReviewFiles = nil
			for _, member := range index.Changes[id].Calls {
				part, found, err := selected.Store.ReadHostCall(workspace, member.ID, false)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("edit receipt interval is unavailable")
				}
				history.ReviewFiles = append(history.ReviewFiles, legacyHistory(part.History).ReviewFiles...)
			}
			receipt := sessionreview.CapturedReceipt(workspace, record.History, history.ReviewFiles)
			if history.ToolName == applyPatchToolName {
				receipt = &changepresentation.Receipt{Text: changepresentation.ReceiptText(workspace, receiptEvidence(history))}
				for _, result := range history.HostResults {
					if result.Tool == applyPatchToolName {
						receipt.Calls = append(receipt.Calls, result.CallID)
					}
				}
				if len(receipt.Calls) == 0 && displayID != "" {
					receipt.Calls = []string{displayID}
				}
			}
			if receipt != nil && receipt.Text != "" {
				activity.Collect(activitypresentation.Supplement{Thread: thread, Source: "edit-receipt\x00" + workspace + "\x00" + callID, Kind: activitypresentation.SupplementEditReceipt, Text: receipt.Text, Calls: receipt.Calls})
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
			return "", err
		}
		slices.Sort(calls)
		calls = slices.Compact(calls)
	}
	var ids []string
	seen := make(map[string]bool)
	for _, derived := range calls {
		record, found, err := selected.Store.LookupHostCall(ctx, workspace, derived)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		if record.ExecOutcome != nil && record.ExecOutcome.SharedWith != "" {
			record, found, err = selected.Store.LookupHostCall(ctx, workspace, record.ExecOutcome.SharedWith)
			if err != nil {
				return "", err
			}
		}
		authored := authoredChangeHistory(legacyHistory(record))
		if len(authored.ReviewFiles) == 0 {
			intervals, err := readExecIntervals(ctx, selected, source, workspace, callID, history.ExecObservation != nil && history.ExecObservation.CodeMode)
			if err != nil {
				return "", err
			}
			if len(intervals) == 0 {
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
	if len(ids) == 0 {
		return "", nil
	}
	// Put every ID before the bounded statistics, including multi-patch cells.
	header := "mchanges " + strings.Join(ids, " ") + " --summary\n"
	summary, err := source.Read(ctx, selected, changes.Query{Workspace: workspace, IDs: ids, View: "summary"})
	if err != nil {
		return "", err
	}
	const truncated = "[summary truncated; run the mchanges command above for full statistics]"
	budget := changepresentation.MaxReceiptDiffBytes - len(header) - len(truncated)
	if len(summary) > budget {
		end := strings.LastIndexByte(summary[:max(0, budget)], '\n')
		summary = summary[:end+1] + truncated
	}
	return header + summary, nil
}

// receiptEvidence translates observation facts into renderer-neutral receipt input.
func receiptEvidence(value mekugiHistory) changepresentation.ReceiptEvidence {
	evidence := changepresentation.ReceiptEvidence{Files: value.ReviewFiles, Source: value.Source, ChangeID: value.ChangeID}
	evidence.Source = binding.EditReceiptSource(value.ToolName, value.Source)
	if value.ExecOutcome != nil {
		evidence.Labels, evidence.DeletedDirectories = value.ExecOutcome.Labels, value.ExecOutcome.DeletedDirectories
	}
	return evidence
}
