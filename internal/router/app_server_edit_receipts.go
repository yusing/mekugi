package router

import "strings"

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
		// arrival order. Other grouped calls retain their status, not a second
		// copy of the receipt or the interpreter source that produced it.
		for index, call := range receipt.calls {
			i, found := entries[[2]string{receipt.thread, call}]
			if !found {
				continue
			}
			entry := v.entries[i]
			text := receipt.text
			if index > 0 {
				text = "Run · included in grouped edit capture"
			}
			if entry.Text != text {
				entry.Text = text
				blocks := parseLiveActivity(entry)
				if len(v.blocks[i]) > 0 {
					for j := range blocks {
						blocks[j].exitCode = v.blocks[i][0].exitCode
					}
				}
				v.entries[i], v.blocks[i], v.runs = entry, blocks, nil
			}
		}
	}
}
