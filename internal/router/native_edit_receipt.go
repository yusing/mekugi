package router

import (
	"context"
	"fmt"
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
			if len(record.History.ReviewFiles) != 0 {
				if receipt := editReceiptText(workspace, record.History); receipt != "" {
					activity.collect(thread, "edit-receipt\x00"+workspace+"\x00"+callID, "tool", receipt)
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
	var managed []string
	budget := maxEditReceiptDiffBytes
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
		if !file.Binary {
			if hunks := editReceiptHunks(file.Diff, &budget); hunks != "" {
				summary += "\n" + toolActivityFenced("diff", hunks)
			}
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
