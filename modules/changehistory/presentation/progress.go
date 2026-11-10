package presentation

import (
	"cmp"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type CaptureProgress struct {
	Key, Caller, Change, Interval string
	Chunks                        int
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
			interval := fmt.Sprintf("%s record=%d chunks=%d..%d", chunks[0].Change, chunks[0].CaptureOrder, seen[key]+1, len(chunks))
			if chunks[0].CaptureOrder == 0 {
				interval = fmt.Sprintf("%s legacy=%q chunks=%d..%d", chunks[0].Change, chunks[0].Key, seen[key]+1, len(chunks))
			}
			if !yield(CaptureProgress{Key: key, Caller: chunks[0].Caller, Change: chunks[0].Change, Interval: interval, Chunks: len(chunks)}) {
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
