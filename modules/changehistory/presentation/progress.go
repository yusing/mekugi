package presentation

import (
	"cmp"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type CaptureProgress struct {
	Key, Caller, Summary string
	Chunks               int
}

// Progress borrows immutable captures, including net-zero and temporary edits.
// Counts, not numeric ID watermarks, detect late and extended publications.
func (p Publication) Progress(callers map[string]bool, seen map[string]int) iter.Seq[CaptureProgress] {
	return func(yield func(CaptureProgress) bool) {
		groups := make(map[string][]*Chunk)
		for chunk := range p.progressCaptures() {
			if chunk.Change != "" && callers[chunk.Caller] && (changes.AuthoredReview(chunk.Review) || changes.GitDiagnosticReview(chunk.Review)) {
				// One change can gain late writer intervals. Each retained
				// record has its own capture identity, even out of ID order.
				key := changes.CaptureKey(chunk.Workspace, chunk.Caller, chunk.Change, chunk.CaptureOrder)
				if key == "" {
					key = chunk.Workspace + "\x00" + chunk.Caller + "\x00" + chunk.Change + "\x00" + chunk.Key
				}
				groups[key] = append(groups[key], chunk)
			}
		}
		keys := make([]string, 0, len(groups))
		for key, chunks := range groups {
			if len(chunks) > seen[key] {
				keys = append(keys, key)
			}
		}
		slices.SortFunc(keys, func(a, b string) int {
			return cmp.Or(cmp.Compare(groups[a][0].CaptureOrder, groups[b][0].CaptureOrder), strings.Compare(a, b))
		})
		for _, key := range keys {
			chunks := groups[key]
			var summary strings.Builder
			view := "--summary"
			if slices.ContainsFunc(chunks, func(chunk *Chunk) bool { return changes.GitDiagnosticReview(chunk.Review) }) {
				view = "--history"
			}
			fmt.Fprintf(&summary, "mchanges %s %s", chunks[0].Change, view)
			for _, chunk := range chunks[:min(8, len(chunks))] {
				file := chunk.Review
				path := pathdisplay.ForWorkspace(chunk.Workspace, cmp.Or(file.AfterPath, file.BeforePath))
				if file.BeforePath != "" && file.AfterPath != "" && file.BeforePath != file.AfterPath {
					path = pathdisplay.Move(chunk.Workspace, file.BeforePath, file.AfterPath)
				}
				if runes := []rune(path); len(runes) > 96 {
					path = string(runes[:96]) + "…"
				}
				counts := "counts unavailable"
				if file.Incomplete == "" && !file.Binary {
					added, removed := file.LineCounts()
					counts = fmt.Sprintf("+%d -%d", added, removed)
				}
				fmt.Fprintf(&summary, "\n%s %s %q", diffmodel.StatusOf(file).ShortCode(), counts, path)
				if changes.GitDiagnosticReview(file) {
					fmt.Fprintf(&summary, " (diagnostic: %.96s)", file.Origin)
				}
			}
			if len(chunks) > 8 {
				fmt.Fprintf(&summary, "\n%d more file captures; use the command above", len(chunks)-8)
			}
			if !yield(CaptureProgress{Key: key, Caller: chunks[0].Caller, Summary: summary.String(), Chunks: len(chunks)}) {
				return
			}
		}
	}
}

func (p Publication) progressCaptures() iter.Seq[*Chunk] {
	return func(yield func(*Chunk) bool) {
		for _, file := range p.Files {
			for i := range file.Chunks {
				if !yield(&file.Chunks[i]) {
					return
				}
			}
		}
		for i := range p.Diagnostics {
			if !yield(&p.Diagnostics[i]) {
				return
			}
		}
	}
}
