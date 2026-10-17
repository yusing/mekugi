package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

func (u *appServerUI) applyCapturedEdits() {
	u.pendingEditReceipts = slices.DeleteFunc(u.pendingEditReceipts, func(receipt *changepresentation.Receipt) bool {
		// Main and Activity receive their host entries together. Keep a grouped
		// receipt until every host identity has arrived, including its anchor.
		u.view.applyCapturedEdits([]*changepresentation.Receipt{receipt})
		u.agents.applyCapturedEdits([]*changepresentation.Receipt{receipt})
		return !slices.ContainsFunc(receipt.Calls, func(call string) bool {
			return !slices.ContainsFunc(u.agents.Entries, func(entry liveActivityRecord) bool {
				return entry.Source.native != nil && entry.Source.native.thread == receipt.Thread && entry.Source.native.item == call
			})
		})
	})
	if u.shell == nil || u.shell.saved.Data == nil {
		return
	}
	u.view.applyCapturedEdits(u.shell.saved.Data.Receipts())
	u.agents.applyCapturedEdits(u.shell.saved.Data.Receipts())
}

// Both audiences reconcile using exact thread/tool identities, regardless of
// whether the retained receipt or the app-server item arrived first. A grouped
// capture is shown once, never attributed separately to each nested command.
func (v *activityAdapter) applyCapturedEdits(receipts []*changepresentation.Receipt) {
	if len(receipts) == 0 {
		return
	}
	entries := make(map[[2]string]int)
	for i, entry := range v.Entries {
		if entry.Source.Kind == "tool" && entry.Source.native != nil {
			entries[[2]string{entry.Source.native.thread, entry.Source.native.item}] = i
		}
	}
	for _, receipt := range receipts {
		if receipt == nil {
			continue
		}
		// Anchor the receipt to the first recorded host call, regardless of
		// arrival order. Other grouped calls retain actual non-edit operations
		// and failures, not a duplicate receipt or a bookkeeping placeholder.
		for index, call := range receipt.Calls {
			i, found := entries[[2]string{receipt.Thread, call}]
			if !found {
				continue
			}
			entry := v.Entries[i].Source
			// Host refreshes replace the native item; unchanged projections already
			// carry this immutable receipt and need no shell classification.
			if entry.native.capturedEdit == receipt {
				continue
			}
			if receipt.Text == "" {
				if entry.native.capturedEdit != nil {
					native := *entry.native
					native.capturedEdit = nil
					entry.native = &native
					entry.Text = activitypresentation.ShellSource(frontends.ShellDisplaySource(appServerDisplayCommand(native.command)), native.commandCwd)
					v.Model.Replace(i, entry, activitypresentation.RetainAnnotations(activityContent(entry).Values(), v.Entries[i].Values))
				}
				continue
			}
			exit := 0
			var batch *activitypresentation.Value
			for _, block := range v.sourceValues(i) {
				if block.BatchExit {
					batch = new(block)
					continue
				}
				if block.ExitCode != 0 {
					exit = block.ExitCode
					break
				}
			}
			text := receipt.Text
			if index == 0 && entry.native.command != "" {
				// Rebuild from the host command so repeated reconciliation never
				// duplicates receipts or drops neighboring operations.
				original := activitypresentation.ShellSource(frontends.ShellDisplaySource(appServerDisplayCommand(entry.native.command)), entry.native.commandCwd)
				paragraphs := activitymodel.Paragraphs(original)
				if len(paragraphs) > 1 {
					var parts []string
					inserted := false
					for _, paragraph := range paragraphs {
						block := activitymodel.ParseOperation(paragraph)
						if slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
							if !inserted {
								parts = append(parts, receipt.Text)
								inserted = true
							}
							continue
						}
						parts = append(parts, paragraph)
					}
					if !inserted {
						parts = append(parts, receipt.Text)
					}
					text = strings.Join(parts, "\n\n")
				}
			}
			if index > 0 {
				var remaining []string
				for _, paragraph := range activitymodel.Paragraphs(entry.Text) {
					block := activitymodel.ParseOperation(paragraph)
					if exit == 0 && (batch == nil || batch.ExitCode == 0) && slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
						continue
					}
					remaining = append(remaining, paragraph)
				}
				text = strings.Join(remaining, "\n\n")
			}
			if entry.Text != text || entry.native.capturedEdit != receipt {
				// Keep the receipt on this view's projection so segment refreshes
				// and dialog reparses cannot fall back to requested edit labels.
				native := *entry.native
				native.capturedEdit = receipt
				entry.native = &native
				entry.Text = text
				blocks := activityContent(entry).Values()
				// Reparsed tracked blocks already carry each segment's own exit.
				// A receipt must not spread one failure to successful siblings.
				if len(v.Entries[i].Values) > 0 && len(entry.native.segments) == 0 {
					for j := range blocks {
						blocks[j].ExitCode = exit
					}
				}
				if batch != nil {
					blocks = activitypresentation.RetainBatchResult(blocks, *batch)
				}
				v.Model.Replace(i, entry, activitypresentation.RetainAnnotations(blocks, v.Entries[i].Values))
			}
		}
	}
}
