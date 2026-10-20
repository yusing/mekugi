package sessionpresentation

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

// Source: apps/mekugi/internal/router/app_server_edit_receipts.go:35:147@45fbe279
// Both audiences reconcile using exact thread/tool identities, regardless of
// whether the retained receipt or the app-server item arrived first. A grouped
// capture is shown once, never attributed separately to each nested command.
func (v *ActivitySource) ApplyCapturedEdits(receipts []*changepresentation.Receipt) {
	if len(receipts) == 0 {
		return
	}
	entries := make(map[[2]string]int)
	for i, entry := range v.Entries {
		if entry.Source.Kind == "tool" && entry.Source.Native != nil {
			entries[[2]string{entry.Source.Native.Thread, entry.Source.Native.Item}] = i
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
			if entry.Native.CapturedEdit == receipt {
				continue
			}
			if receipt.Text == "" {
				if entry.Native.CapturedEdit != nil {
					native := *entry.Native
					native.CapturedEdit = nil
					entry.Native = &native
					entry.Text = activitypresentation.ShellSource(frontends.ShellDisplaySource(DisplayCommand(native.Command)), native.CommandCwd)
					v.Model.Replace(i, entry, activitypresentation.RetainAnnotations(ActivityContent(entry).Values(), v.Entries[i].Values))
				}
				continue
			}
			exit := 0
			var batch *activitypresentation.Value
			for _, block := range v.SourceValues(i) {
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
			if index == 0 && entry.Native.Command != "" {
				// Rebuild from the host command so repeated reconciliation never
				// duplicates receipts or drops neighboring operations.
				original := activitypresentation.ShellSource(frontends.ShellDisplaySource(DisplayCommand(entry.Native.Command)), entry.Native.CommandCwd)
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
			if entry.Text != text || entry.Native.CapturedEdit != receipt {
				// Keep the receipt on this view's projection so segment refreshes
				// and dialog reparses cannot fall back to requested edit labels.
				native := *entry.Native
				native.CapturedEdit = receipt
				entry.Native = &native
				entry.Text = text
				blocks := ActivityContent(entry).Values()
				// Reparsed tracked blocks already carry each segment's own exit.
				// A receipt must not spread one failure to successful siblings.
				if len(v.Entries[i].Values) > 0 && len(entry.Native.Segments) == 0 {
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
