package mekugi

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// ReviewMerge is the result of replaying or undoing one captured review
// against current content. Rejected hunks are left out of Content.
type ReviewMerge struct {
	Content string
	// Conflicts counts regions written with git-style conflict markers.
	Conflicts int
	// Satisfied counts hunks whose requested side was already present.
	Satisfied int
	// Rejected holds captured hunks, in review format, that could not be located.
	Rejected []string
}

// Merge applies the captured hunks to current content: forward replays the
// recorded change and reverse undoes it. A hunk is located exactly near its
// recorded position first. Otherwise its surrounding context anchors a region
// that is merged three ways, leaving conflict markers where the region and the
// requested side both changed. A hunk without locatable context is rejected
// instead of guessed. Captures hold only hunk context, so no file is read.
func (file ReviewFile) Merge(current string, reverse bool, label string) (ReviewMerge, error) {
	if file.Incomplete != "" {
		return ReviewMerge{}, fmt.Errorf("incomplete history: %s", file.Incomplete)
	}
	if file.Binary {
		return ReviewMerge{}, errors.New("binary content is not retained")
	}
	hunks, err := parseReviewHunks(file, false)
	if err != nil {
		return ReviewMerge{}, err
	}
	lines := reviewLines(current)
	var result ReviewMerge
	var output []string
	cursor, offset := 0, 0
	for _, hunk := range hunks {
		from, to, start := hunk.sides(reverse)
		lead, trail := hunk.contextRuns()
		expected := max(cursor, start+offset)
		// A side without a final newline ends the file; splicing it before
		// other lines would join them.
		atEnd := len(to) != 0 && !strings.HasSuffix(to[len(to)-1], "\n")
		if at, ok := nearestReviewRun(lines, from, cursor, expected); ok && (!atEnd || at+len(from) == len(lines)) {
			output = append(append(output, lines[cursor:at]...), to...)
			cursor, offset = at+len(from), at-start
			continue
		}
		if first, last, baseStart, baseEnd, ok := anchorReviewRegion(lines, from, lead, trail, cursor, expected+lead); ok {
			theirs := to[baseStart : len(to)-(len(from)-baseEnd)]
			if slices.Equal(lines[first:last], theirs) {
				result.Satisfied++
			}
			merged, conflicts := mergeReviewLines(from[baseStart:baseEnd], lines[first:last], theirs, label)
			output = append(append(output, lines[cursor:first]...), merged...)
			cursor, offset = last, first-baseStart-start
			result.Conflicts += conflicts
			continue
		}
		// Without its own context, a requested side counts as already present
		// only near the recorded position, never as a copy elsewhere.
		if at, ok := nearestReviewRun(lines, to, cursor, expected); ok && len(to) != 0 && distance(at, expected) <= len(to) {
			output = append(output, lines[cursor:at+len(to)]...)
			cursor, offset = at+len(to), at-start
			result.Satisfied++
			continue
		}
		result.Rejected = append(result.Rejected, hunk.text())
	}
	output = append(output, lines[cursor:]...)
	result.Content = strings.Join(output, "")
	return result, nil
}

// sides returns the rows the hunk expects to find, the rows that replace them,
// and the recorded zero-based position of the expected rows.
func (hunk ReviewHunk) sides(reverse bool) (from, to []string, start int) {
	var before, after []string
	for _, row := range hunk.Rows {
		if row.Kind != '+' {
			before = append(before, row.Text)
		}
		if row.Kind != '-' {
			after = append(after, row.Text)
		}
	}
	if reverse {
		return after, before, hunk.AfterStart
	}
	return before, after, hunk.BeforeStart
}

func (hunk ReviewHunk) contextRuns() (lead, trail int) {
	for lead < len(hunk.Rows) && hunk.Rows[lead].Kind == ' ' {
		lead++
	}
	for trail < len(hunk.Rows)-lead && hunk.Rows[len(hunk.Rows)-1-trail].Kind == ' ' {
		trail++
	}
	return lead, trail
}

func (hunk ReviewHunk) text() string {
	before, after, _ := hunk.sides(false)
	var text strings.Builder
	fmt.Fprintf(&text, "@@ -%s +%s @@\n",
		reviewRange(hunk.BeforeStart, hunk.BeforeStart+len(before)),
		reviewRange(hunk.AfterStart, hunk.AfterStart+len(after)))
	for _, row := range hunk.Rows {
		writeReviewLines(&text, row.Kind, []string{row.Text})
	}
	return text.String()
}

// nearestReviewRun finds run in lines at or after cursor, closest to expected.
// An empty run matches only an empty remainder: captures always include
// context unless the recorded side was empty.
func nearestReviewRun(lines, run []string, cursor, expected int) (int, bool) {
	if len(run) == 0 {
		return cursor, cursor == len(lines)
	}
	best := -1
	for at := cursor; at+len(run) <= len(lines); at++ {
		if slices.Equal(lines[at:at+len(run)], run) && (best < 0 || distance(at, expected) < distance(best, expected)) {
			best = at
		}
	}
	return best, best >= 0
}

// anchorReviewRegion finds the current region between the hunk's leading and
// trailing context. When context no longer matches, a shorter run from either
// edge of each context anchors instead, and the unmatched context joins the
// merged region. Each side keeps at least one line unless the hunk touches the
// start or end of the file. The candidate nearest the recorded position wins
// across fuzz levels, so a distant copy of the context cannot outrank the
// drifted region itself; ties keep the least fuzz. It returns the region and
// its bounds within from.
func anchorReviewRegion(lines, from []string, lead, trail, cursor, expected int) (first, last, baseStart, baseEnd int, ok bool) {
	span := 2*(len(from)-lead-trail) + 20
	best, bestDistance := -1, 0
	for fuzz := 0; fuzz == 0 || fuzz < max(lead, trail); fuzz++ {
		for _, leading := range reviewAnchors(0, lead, fuzz, true) {
			for _, trailing := range reviewAnchors(len(from)-trail, len(from), fuzz, false) {
				anchor := from[leading[0]:leading[1]]
				for at := cursor; at+len(anchor) <= len(lines); at++ {
					start := at + len(anchor)
					if lead == 0 && start != 0 || !slices.Equal(lines[at:start], anchor) {
						continue
					}
					end := -1
					if trail == 0 {
						end = len(lines)
					} else {
						tail := from[trailing[0]:trailing[1]]
						for candidate := start; candidate+len(tail) <= len(lines) && candidate-start <= span; candidate++ {
							if slices.Equal(lines[candidate:candidate+len(tail)], tail) {
								end = candidate
								break
							}
						}
					}
					target := expected - lead + leading[1]
					if end >= 0 && (best < 0 || distance(start, target) < bestDistance) {
						best, bestDistance = start, distance(start, target)
						first, last, baseStart, baseEnd = start, end, leading[1], trailing[0]
					}
				}
			}
		}
	}
	return first, last, baseStart, baseEnd, best >= 0
}

// reviewAnchors lists context runs within [start, end) with fuzz lines
// dropped, keeping the edge nearest the change first. An empty context is a
// file boundary and has no shorter form.
func reviewAnchors(start, end, fuzz int, leading bool) [][2]int {
	keep := max(1, end-start-fuzz)
	if start == end || keep == end-start {
		return [][2]int{{start, end}}
	}
	inner, outer := [2]int{end - keep, end}, [2]int{start, start + keep}
	if !leading {
		inner, outer = [2]int{start, start + keep}, [2]int{end - keep, end}
	}
	if inner == outer {
		return [][2]int{inner}
	}
	return [][2]int{inner, outer}
}

func distance(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// mergeReviewLines is a line-level diff3 merge. Regions changed only on one
// side take that side; identical changes are taken once; others conflict.
func mergeReviewLines(base, ours, theirs []string, label string) ([]string, int) {
	matchOurs, matchTheirs := reviewMatches(base, ours), reviewMatches(base, theirs)
	var output []string
	conflicts := 0
	b, o, t := 0, 0, 0
	for {
		for b < len(base) && matchOurs[b] == o && matchTheirs[b] == t {
			output = append(output, base[b])
			b, o, t = b+1, o+1, t+1
		}
		if b == len(base) && o == len(ours) && t == len(theirs) {
			return output, conflicts
		}
		next := b
		for next < len(base) && (matchOurs[next] < 0 || matchTheirs[next] < 0) {
			next++
		}
		nextOurs, nextTheirs := len(ours), len(theirs)
		if next < len(base) {
			nextOurs, nextTheirs = matchOurs[next], matchTheirs[next]
		}
		baseChunk, oursChunk, theirsChunk := base[b:next], ours[o:nextOurs], theirs[t:nextTheirs]
		switch {
		case slices.Equal(oursChunk, baseChunk):
			output = append(output, theirsChunk...)
		case slices.Equal(theirsChunk, baseChunk) || slices.Equal(oursChunk, theirsChunk):
			output = append(output, oursChunk...)
		default:
			conflicts++
			output = append(output, "<<<<<<< workspace\n")
			output = appendTerminated(output, oursChunk)
			output = append(output, "=======\n")
			output = appendTerminated(output, theirsChunk)
			output = append(output, ">>>>>>> "+label+"\n")
		}
		b, o, t = next, nextOurs, nextTheirs
	}
}

func reviewMatches(a, b []string) []int {
	matches := make([]int, len(a))
	for i := range matches {
		matches[i] = -1
	}
	for _, block := range difflib.NewMatcher(a, b).GetMatchingBlocks() {
		for k := range block.Size {
			matches[block.A+k] = block.B + k
		}
	}
	return matches
}

// A conflict marker must start its own line.
func appendTerminated(output, lines []string) []string {
	output = append(output, lines...)
	if len(lines) != 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
		output[len(output)-1] += "\n"
	}
	return output
}

// Consistent reports whether content agrees with every source line the
// composition has learned at its current positions. An edit made outside the
// composed captures breaks this, even when no captured hunk would touch it.
func (c *ReviewComposition) Consistent(content string) bool {
	lines := reviewLines(content)
	for position, line := range c.current {
		if position >= len(lines) || lines[position] != line {
			return false
		}
	}
	return true
}
