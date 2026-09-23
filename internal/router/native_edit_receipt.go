package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/pathdisplay"
)

// publishEditReceipt reads committed stock-edit evidence. It never accepts a
// caller-supplied diff and never participates in edit execution.
func (s *mekugiReplayStore) publishEditReceipt(ctx context.Context, workspace, thread, callID string, activity *subagentActivity) error {
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
			if record.History.Applied && record.History.TranslationError == "" {
				activity.collect(thread, "edit-receipt\x00"+workspace+"\x00"+callID, "tool", editReceiptText(workspace, record.History))
			}
			s.notifyLiveDiff(index, map[string][]trackedCall{id: {call}})
			return nil
		}
		return fmt.Errorf("edit receipt does not belong to publishing thread")
	})
}

// editReceiptText summarizes a confirmed edit, one line per file. Command
// records name the command that produced each file.
func editReceiptText(workspace string, history mekugiHistory) string {
	exec := history.ExecOutcome
	var summaries []string
	var managed []string
	for _, file := range history.ReviewFiles {
		action := file.Action().Title()
		path := file.AfterPath
		if path == "" {
			path = file.BeforePath
		}
		path = pathdisplay.ForWorkspace(workspace, path)
		if file.Origin != "" {
			managed = append(managed, commentaryCode(path))
			continue
		}
		if file.Incomplete != "" {
			summaries = append(summaries, fmt.Sprintf("%s %s: incomplete history; line counts unavailable", action, commentaryCode(path)))
			continue
		}
		summary := action + " " + commentaryCode(path)
		if file.OriginNote != "" {
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
		}
		summaries = append(summaries, summary)
	}
	if len(managed) != 0 {
		names := managed[:min(3, len(managed))]
		label := strings.Join(names, ", ")
		if len(managed) > len(names) {
			label += ", …"
		}
		summaries = append(summaries, fmt.Sprintf("+ %d tool-managed files (%s)", len(managed), label))
	}
	return strings.Join(summaries, "\n\n")
}
