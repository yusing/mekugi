package presentation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

// ReceiptEvidence contains captured facts needed for a source-text edit summary.
// It carries no request, backend or retained-store handles.
type ReceiptEvidence struct {
	Files                      []diffmodel.ReviewFile
	Labels, DeletedDirectories []string
	Source, ChangeID           string
}

// Source: apps/mekugi/internal/router/native_edit_receipt.go@6f68e18a.
// Receipt diffs are display-only and stay well inside one publication.
const (
	MaxReceiptDiffBytes     = 8 << 10
	maxEditReceiptFileLines = 80
)

// ReceiptText summarizes captured files and bounded hunks without consulting
// current workspace content or changing the retained evidence.
func ReceiptText(workspace string, evidence ReceiptEvidence) string {
	var summaries []string
	reasons := make(map[string]int)
	budget := MaxReceiptDiffBytes
	directories := make(map[string]int)
	roots := evidence.DeletedDirectories
	for _, root := range roots {
		count, removed := 0, true
		for _, file := range evidence.Files {
			if !changes.PathWithin(file.BeforePath, root) {
				continue
			}
			// Missing content does not undo the confirmed removal of a path.
			if file.AfterPath != "" || !changes.AuthoredReview(file) {
				removed = false
			}
			if !file.Directory {
				count++
			}
		}
		if count > 0 && removed {
			directories[root] = count
		}
	}
	for _, file := range evidence.Files {
		if !changes.AuthoredReview(file) {
			continue
		}
		if file.Incomplete != "" {
			reasons[file.Incomplete]++
		}
		grouped := false
		for _, root := range roots {
			count, compact := directories[root]
			if !compact || !changes.PathWithin(file.BeforePath, root) {
				continue
			}
			if count > 0 {
				noun := "files"
				if count == 1 {
					noun = "file"
				}
				summaries = append(summaries, fmt.Sprintf("Delete %s • %d %s · %s", markdownsyntax.QuoteCode(strings.TrimSuffix(pathdisplay.ForWorkspace(workspace, root), "/")+"/"), count, noun, strings.Join(evidence.Labels, ", ")))
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
			summaries = append(summaries, action+" "+markdownsyntax.QuoteCode(path+"/"))
			continue
		}
		if file.Incomplete != "" {
			continue
		}
		summary := action + " " + markdownsyntax.QuoteCode(path)
		if file.OriginNote != "" && !receiptNamesSharedOrigin(evidence.Labels, file.OriginNote) {
			summary += " (" + file.OriginNote + ")"
		}
		if file.CopyFrom != "" {
			summary += " (copy of " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(workspace, file.CopyFrom)) + ")"
		}
		if file.Binary {
			summary += " binary"
		} else {
			added, removed := file.LineCounts()
			summary += fmt.Sprintf(" +%d -%d", added, removed)
		}
		if len(evidence.Labels) != 0 {
			summary += " · " + strings.Join(evidence.Labels, ", ")
		} else if evidence.Source != "" {
			summary += " · " + evidence.Source
		}
		if !file.Binary {
			if hunks := editReceiptHunks(file.Diff, &budget); hunks != "" {
				summary += "\n" + markdownsyntax.SourceFence("diff", hunks)
			}
		}
		summaries = append(summaries, summary)
	}
	if len(reasons) != 0 {
		// Capture omissions are evidence gaps, not confirmed edits. Keep the
		// full path/reason records in mchanges without flooding Activity.
		summary := "Capture · " + captureGapSummary(reasons)
		if evidence.ChangeID != "" {
			summary += " · " + markdownsyntax.QuoteCode("mchanges "+evidence.ChangeID+" --summary")
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

// Command labels already naming every shared-origin tool make its note redundant.
func receiptNamesSharedOrigin(labels []string, note string) bool {
	tools, ok := strings.CutSuffix(note, changes.SharedOriginSuffix)
	if !ok {
		return false
	}
	for tool := range strings.SplitSeq(tools, ", ") {
		if !slices.Contains(labels, tool) {
			return false
		}
	}
	return true
}
