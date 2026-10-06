package router

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/pathdisplay"
)

// Receipt diffs are display-only and stay well inside one publication.
const (
	maxEditReceiptDiffBytes = 8 << 10
	maxEditReceiptFileLines = 80
)

// publishEditReceipt reads committed filesystem changes. It never accepts a
// caller-supplied diff or participates in edit execution. Command exit status
// does not change the filesystem effects that this receipt describes.
func (s *mekugiReplayStore) publishEditReceipt(ctx context.Context, workspace, thread, callID string, activity *subagentActivity, displayID string) error {
	s = s.scoped(ctx)
	return s.locked(ctx, func() error {
		if scope, found, err := s.readHandleScope(thread); err != nil {
			return err
		} else if found {
			s.session.Namespace = scope.Namespace
		}
		record, found, err := s.read(workspace, callID, false)
		if err != nil {
			return err
		}
		if !found || record.History.ToolName != applyPatchToolName && (record.History.ToolName != nativeExecCommandToolName || record.History.ExecOutcome == nil) ||
			record.History.ExecutingThread != thread {
			return fmt.Errorf("edit receipt not found")
		}
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		id := record.History.ChangeID
		for _, call := range index.Changes[id].Calls {
			if call.ID != callID || call.Thread != thread {
				continue
			}
			if len(record.History.ReviewFiles) != 0 {
				if receipt := editReceiptText(workspace, record.History); receipt != "" {
					// The observed receipt and the invocation share one display identity.
					// An opaque command stays Run until its actual effects are known.
					identity := displayID
					if identity == "" {
						identity = callID
					}
					activity.collectEvent(activityEvent{thread: thread, source: "edit-receipt\x00" + workspace + "\x00" + callID, kind: "tool", text: receipt, callID: identity})
				}
			}
			return nil
		}
		return fmt.Errorf("edit receipt does not belong to publishing thread")
	})
}

// editReceiptText summarizes each captured file with its line counts and a
// bounded copy of its captured hunks. Command records name the command that
// produced each file.
func editReceiptText(workspace string, history mekugiHistory) string {
	exec := history.ExecOutcome
	var summaries []string
	reasons := make(map[string]int)
	budget := maxEditReceiptDiffBytes
	directories := make(map[string]int)
	var roots []string
	if exec != nil {
		roots = exec.DeletedDirectories
		for _, root := range exec.DeletedDirectories {
			count, complete := 0, true
			for _, file := range history.ReviewFiles {
				if !execPathWithin(file.BeforePath, root) {
					continue
				}
				if file.Incomplete != "" || file.AfterPath != "" || !authoredReview(file) {
					complete = false
				}
				if !file.Directory {
					count++
				}
			}
			if count > 0 && complete {
				directories[root] = count
			}
		}
	}
	for _, file := range history.ReviewFiles {
		if !authoredReview(file) {
			continue
		}
		grouped := false
		for _, root := range roots {
			count, compact := directories[root]
			if !compact || !execPathWithin(file.BeforePath, root) {
				continue
			}
			if count > 0 {
				noun := "files"
				if count == 1 {
					noun = "file"
				}
				summaries = append(summaries, fmt.Sprintf("Delete %s • %d %s · %s", commentaryCode(strings.TrimSuffix(pathdisplay.ForWorkspace(workspace, root), "/")+"/"), count, noun, strings.Join(exec.Labels, ", ")))
				directories[root] = 0
			}
			grouped = true
			break
		}
		if grouped {
			continue
		}
		action := file.Action().Title()
		path := file.AfterPath
		if path == "" {
			path = file.BeforePath
		}
		path = pathdisplay.ForWorkspace(workspace, path)
		if action == "Move" {
			path = pathdisplay.Move(workspace, file.BeforePath, file.AfterPath)
		}
		if file.Directory {
			summaries = append(summaries, action+" "+commentaryCode(path+"/"))
			continue
		}
		if file.Incomplete != "" {
			reasons[file.Incomplete]++
			continue
		}
		summary := action + " " + commentaryCode(path)
		if file.OriginNote != "" && !receiptNamesSharedOrigin(exec, file.OriginNote) {
			summary += " (" + file.OriginNote + ")"
		}
		if file.CopyFrom != "" {
			summary += " (copy of " + commentaryCode(pathdisplay.ForWorkspace(workspace, file.CopyFrom)) + ")"
		}
		if file.Binary {
			summary += " binary"
		} else {
			added, removed := file.LineCounts()
			summary += fmt.Sprintf(" +%d -%d", added, removed)
		}
		if exec != nil && len(exec.Labels) != 0 {
			summary += " · " + strings.Join(exec.Labels, ", ")
		} else if history.ToolName == applyPatchToolName {
			summary += " · apply_patch"
		}
		if !file.Binary {
			if hunks := editReceiptHunks(file.Diff, &budget); hunks != "" {
				summary += "\n" + toolActivityFenced("diff", hunks)
			}
		}
		summaries = append(summaries, summary)
	}
	if len(reasons) != 0 {
		// Capture omissions are evidence gaps, not confirmed edits. Keep the
		// full path/reason records in mchanges without flooding Activity.
		summary := "Capture · " + captureGapSummary(reasons)
		if history.ChangeID != "" {
			summary += " · " + commentaryCode("mchanges "+history.ChangeID+" --summary")
		}
		summaries = append(summaries, summary)
	}
	return strings.Join(summaries, "\n\n")
}

// Keep Activity summaries useful without flooding them with
// per-path errors. Full reasons remain on the retained per-path review records.
func captureGapSummary(reasons map[string]int) string {
	keys := make([]string, 0, len(reasons))
	total := 0
	for reason, count := range reasons {
		keys = append(keys, reason)
		total += count
	}
	slices.SortFunc(keys, func(a, b string) int {
		if reasons[a] != reasons[b] {
			return reasons[b] - reasons[a]
		}
		return strings.Compare(a, b)
	})
	var parts []string
	for _, reason := range keys[:min(3, len(keys))] {
		text := []rune(reason)
		if len(text) > 120 {
			text = append(text[:120], '…')
		}
		parts = append(parts, fmt.Sprintf("%d × %q", reasons[reason], string(text)))
	}
	if len(keys) > 3 {
		parts = append(parts, fmt.Sprintf("other reasons: %d", len(keys)-3))
	}
	noun := "paths"
	if total == 1 {
		noun = "path"
	}
	return fmt.Sprintf("incomplete evidence for %d %s (not confirmed edits): %s", total, noun, strings.Join(parts, "; "))
}

// editReceiptHunks keeps hunk rows only; file headers repeat the summary.
func editReceiptHunks(diff string, budget *int) string {
	_, hunks, ok := strings.Cut(diff, "\n@@ ")
	if !ok || *budget <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix("@@ "+hunks, "\n"), "\n")
	var kept strings.Builder
	shown := 0
	for _, line := range lines {
		if shown == maxEditReceiptFileLines || kept.Len()+len(line)+1 > *budget {
			break
		}
		kept.WriteString(line)
		kept.WriteByte('\n')
		shown++
	}
	if hidden := len(lines) - shown; hidden > 0 {
		fmt.Fprintf(&kept, "… %d more diff lines\n", hidden)
	}
	*budget -= kept.Len()
	return kept.String()
}

// agentEditNotice is projected only after observation records are durable. The
// original host result remains a separate content part and is retained verbatim.
func (s *mekugiReplayStore) agentEditNotice(ctx context.Context, workspace, callID string, history mekugiHistory) (string, error) {
	var calls []string
	for index := range history.NativePatches {
		calls = append(calls, nativePatchDerivedCallID(callID, index))
	}
	if history.ExecObservation != nil {
		calls = append(calls, execDerivedCallID(callID, history.ExecObservation.CodeMode))
	}
	if history.ResolvedBaseline != nil {
		// Dynamic nested calls are unknown in the immutable pre-cell carrier.
		// Find their durable derived attempts, not the disposable native trace.
		scoped := s.scoped(ctx)
		if err := scoped.locked(ctx, func() error {
			index, err := scoped.readChangeIndex(workspace)
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
		record, found, err := s.lookup(ctx, workspace, derived)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		if record.ExecOutcome != nil && record.ExecOutcome.SharedWith != "" {
			record, found, err = s.lookup(ctx, workspace, record.ExecOutcome.SharedWith)
			if err != nil {
				return "", err
			}
		}
		record = authoredChangeHistory(record)
		if len(record.ReviewFiles) == 0 {
			continue
		}
		id := record.ChangeID
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
	summary, err := s.readChanges(ctx, changeReadOptions{workspace: workspace, ids: ids, view: "summary"})
	if err != nil {
		return "", err
	}
	const truncated = "[summary truncated; run the mchanges command above for full statistics]"
	budget := maxEditReceiptDiffBytes - len(header) - len(truncated)
	if len(summary) > budget {
		end := strings.LastIndexByte(summary[:max(0, budget)], '\n')
		summary = summary[:end+1] + truncated
	}
	return header + summary, nil
}

// receiptNamesSharedOrigin reports whether the receipt's command labels
// already name every tool in a shared-origin note, which then only repeats
// them on each row.
func receiptNamesSharedOrigin(exec *execOutcome, note string) bool {
	tools, ok := strings.CutSuffix(note, sharedOriginSuffix)
	if !ok || exec == nil {
		return false
	}
	for tool := range strings.SplitSeq(tools, ", ") {
		if !slices.Contains(exec.Labels, tool) {
			return false
		}
	}
	return true
}
