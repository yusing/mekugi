package router

import (
	"slices"
	"strings"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Retain only the display receipt and durable host identities, not scripts or
// another filesystem snapshot. The live diff loader owns replay and scope.
type capturedActivityEdit struct {
	thread string
	calls  []string
	text   string
}

func capturedEditActivity(workspace string, history mekugiHistory) *capturedActivityEdit {
	if history.ExecOutcome == nil {
		return nil
	}
	receipt := &capturedActivityEdit{thread: history.ExecutingThread, text: editReceiptText(workspace, history)}
	if receipt.text == "" {
		return nil
	}
	if !history.ExecOutcome.CodeMode {
		if call, ok := strings.CutSuffix(history.CorrelationID, "\x00exec"); ok {
			receipt.calls = append(receipt.calls, call)
		}
	}
	for _, result := range history.HostResults {
		if result.Tool == "exec_command" {
			receipt.calls = append(receipt.calls, result.CallID)
		}
	}
	return receipt
}

func (u *appServerUI) applyCapturedEdits() {
	if u.shell == nil || u.shell.diff.data == nil {
		return
	}
	u.view.applyCapturedEdits(u.shell.diff.data)
	u.agents.applyCapturedEdits(u.shell.diff.data)
}

// Both audiences reconcile using exact thread/tool identities, regardless of
// whether the retained receipt or the app-server item arrived first. A grouped
// capture is shown once, never attributed separately to each nested command.
func (v *liveActivityView) applyCapturedEdits(data *liveDiffData) {
	entries := make(map[[2]string]int)
	for i, entry := range v.entries {
		if entry.Kind == "tool" && entry.native != nil {
			entries[[2]string{entry.native.thread, entry.native.item}] = i
		}
	}
	for _, key := range data.order {
		receipt := data.attempts[key].receipt
		if receipt == nil {
			continue
		}
		// Anchor the receipt to the first recorded host call, regardless of
		// arrival order. Other grouped calls retain actual non-edit operations
		// and failures, not a duplicate receipt or a bookkeeping placeholder.
		for index, call := range receipt.calls {
			i, found := entries[[2]string{receipt.thread, call}]
			if !found {
				continue
			}
			entry := v.entries[i]
			exit := 0
			var batch *activityui.Block
			for _, block := range v.blocks[i] {
				if block.BatchExit {
					batch = new(block)
					continue
				}
				if block.ExitCode != 0 {
					exit = block.ExitCode
					break
				}
			}
			text := receipt.text
			if index == 0 && entry.native.command != "" {
				// Rebuild from the host command so repeated reconciliation never
				// duplicates receipts or drops neighboring operations.
				original := toolActivityShell(appServerDisplayCommand(entry.native.command))
				paragraphs := activityui.Paragraphs(original)
				if len(paragraphs) > 1 {
					var parts []string
					inserted := false
					for _, paragraph := range paragraphs {
						block := activityui.ParseOperation(paragraph)
						if slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
							if !inserted {
								parts = append(parts, receipt.text)
								inserted = true
							}
							continue
						}
						parts = append(parts, paragraph)
					}
					if !inserted {
						parts = append(parts, receipt.text)
					}
					text = strings.Join(parts, "\n\n")
				}
			}
			if index > 0 {
				var remaining []string
				for _, paragraph := range activityui.Paragraphs(entry.Text) {
					block := activityui.ParseOperation(paragraph)
					if exit == 0 && batch == nil && slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
						continue
					}
					remaining = append(remaining, paragraph)
				}
				text = strings.Join(remaining, "\n\n")
			}
			if entry.Text != text {
				entry.Text = text
				blocks := parseLiveActivity(entry)
				// Reparsed tracked blocks already carry each segment's own exit.
				// A receipt must not spread one failure to successful siblings.
				if len(v.blocks[i]) > 0 && len(entry.native.segments) == 0 {
					for j := range blocks {
						blocks[j].ExitCode = exit
					}
				}
				if batch != nil {
					blocks = append(blocks, *batch)
				}
				v.entries[i], v.blocks[i], v.runs = entry, blocks, nil
			}
		}
	}
}
