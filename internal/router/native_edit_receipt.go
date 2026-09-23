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
		if !found || record.History.ToolName != applyPatchToolName || record.History.ExecutingThread != thread {
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
				var summaries []string
				for _, file := range record.History.ReviewFiles {
					action := file.Action().Title()
					path := file.AfterPath
					if path == "" {
						path = file.BeforePath
					}
					path = pathdisplay.ForWorkspace(workspace, path)
					if file.Incomplete != "" {
						summaries = append(summaries, fmt.Sprintf("%s %s: incomplete history; line counts unavailable", action, commentaryCode(path)))
						continue
					}
					added, removed := file.LineCounts()
					summaries = append(summaries, fmt.Sprintf("%s %s +%d -%d", action, commentaryCode(path), added, removed))
				}
				activity.collect(thread, "edit-receipt\x00"+workspace+"\x00"+callID, "tool", strings.Join(summaries, "\n\n"))
			}
			s.notifyLiveDiff(index, map[string][]trackedCall{id: {call}})
			return nil
		}
		return fmt.Errorf("edit receipt does not belong to publishing thread")
	})
}
