package projection

import (
	"fmt"
	"strings"
	"unicode/utf8"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/modules/outputdeduplication/internal/matcher"
)

const (
	editCandidates = 4
	editWork       = 2 << 20
	editDistance   = 128
)

// Coordinates always address the unchanged complete source, never earlier edits.
type edit struct {
	start, end int
	text       string
}

func (p *Pass) editProjection(header, body, baseline string) (Result, bool) {
	if p.savings.failed || len(body) > matcher.WindowBytes || !utf8.ValidString(body) {
		return Result{}, false
	}
	work := editWork
	var best Result
	var bestTokens int64
	originalTokens := int64(-1)
	haveBaseline := false
	p.index.Complete(editCandidates, func(source int, raw []byte) {
		if work <= 0 || !utf8.Valid(raw) {
			return
		}
		base := string(raw)
		edits, ok := planEdits(base, body, &work)
		if !ok || len(edits) == 0 {
			return
		}
		rebuilt, repeated, lines := reconstruct(base, body, edits)
		if rebuilt != body || repeated < matcher.Threshold {
			return
		}
		var text strings.Builder
		text.WriteString(header)
		fmt.Fprintf(&text, "See O%d with byte edits: [start,end,text] replaces 0-based UTF-8 [start,end); [[offsets],text] inserts; JSON text; other bytes unchanged:\n", source)
		insertions := make(map[string][]int)
		for _, e := range edits {
			if e.start == e.end {
				insertions[e.text] = append(insertions[e.text], e.start)
			}
		}
		for _, e := range edits {
			positions := insertions[e.text]
			if e.start == e.end && len(positions) > 1 && e.start != positions[0] {
				continue
			}
			payload, err := json.Marshal(e.text)
			if err != nil {
				return
			}
			if e.start == e.end && len(positions) > 1 {
				coordinates, _ := json.Marshal(positions)
				fmt.Fprintf(&text, "[%s,%s]\n", coordinates, payload)
			} else {
				fmt.Fprintf(&text, "[%d,%d,%s]\n", e.start, e.end, payload)
			}
		}
		candidate := text.String()
		if !haveBaseline {
			bestTokens = p.savings.count(baseline)
			haveBaseline = true
		}
		tokens := p.savings.count(candidate)
		if p.savings.failed || tokens >= bestTokens {
			return
		}
		if originalTokens < 0 {
			originalTokens = p.savings.count(header + body)
		}
		if p.savings.failed || originalTokens-tokens < minimumTokenSavings {
			return
		}
		bestTokens = tokens
		best = Result{Text: candidate, Occurrences: 1, BodyBytes: len(body), RepeatedBytes: repeated, RepeatedLines: lines}
	})
	// A partial search must never make a budget overrun change the representation.
	return best, work > 0 && !p.savings.failed && best.Text != ""
}

// planEdits uses a bounded Myers frontier over runes, so JSON payloads never
// split UTF-8. Both input scanning and frontier/snake work consume one budget
// shared by all candidates. Large edit distances retain the existing projection.
func planEdits(base, body string, work *int) ([]edit, bool) {
	*work -= len(base) + len(body)
	if *work <= 0 {
		return nil, false
	}
	a, b := []rune(base), []rune(body)
	prefix := 0
	for prefix < min(len(a), len(b)) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < min(len(a), len(b))-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	xs, ys := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	if len(xs) == 0 && len(ys) == 0 {
		return nil, true
	}
	var trace [][]int
	for d := 0; d <= min(editDistance, len(xs)+len(ys)); d++ {
		*work -= 2*d + 1
		if *work <= 0 {
			return nil, false
		}
		v := make([]int, 2*d+1)
		for k := -d; k <= d; k += 2 {
			x := 0
			if d > 0 {
				previous := trace[d-1]
				if k == -d || k != d && previous[d-1+k-1] < previous[d-1+k+1] {
					x = previous[d-1+k+1]
				} else {
					x = previous[d-1+k-1] + 1
				}
			}
			y := x - k
			for x < len(xs) && y < len(ys) && xs[x] == ys[y] {
				x++
				y++
				*work--
				if *work <= 0 {
					return nil, false
				}
			}
			v[d+k] = x
			if x >= len(xs) && y >= len(ys) {
				trace = append(trace, v)
				edits := frontierEdits(a, b, prefix, len(xs), len(ys), trace)
				normalizeInsertions(base, edits, work)
				return edits, *work > 0
			}
		}
		trace = append(trace, v)
	}
	return nil, false
}

// Right-align equivalent insertions through repeated text. This favors intact
// source rows and lets identical whitespace insertions share one payload.
func normalizeInsertions(base string, edits []edit, work *int) {
	for i := range edits {
		e := &edits[i]
		if e.start != e.end || e.text == "" {
			continue
		}
		limit := len(base)
		if i+1 < len(edits) {
			limit = edits[i+1].start
		}
		rotation := 0
		for e.start < limit && *work > 0 {
			r, width := utf8.DecodeRuneInString(e.text[rotation:])
			other, size := utf8.DecodeRuneInString(base[e.start:limit])
			*work -= size
			if r != other {
				break
			}
			e.start += size
			e.end += size
			rotation = (rotation + width) % len(e.text)
		}
		if rotation > 0 {
			e.text = e.text[rotation:] + e.text[:rotation]
		}
	}
}

func frontierEdits(a, b []rune, prefix, x, y int, trace [][]int) []edit {
	var reverse []byte
	for d := len(trace) - 1; d > 0; d-- {
		k := x - y
		previous := trace[d-1]
		previousK := k - 1
		if k == -d || k != d && previous[d-1+k-1] < previous[d-1+k+1] {
			previousK = k + 1
		}
		previousX := previous[d-1+previousK]
		previousY := previousX - previousK
		for x > previousX && y > previousY {
			reverse = append(reverse, '=')
			x--
			y--
		}
		if x == previousX {
			reverse = append(reverse, '+')
			y--
		} else {
			reverse = append(reverse, '-')
			x--
		}
	}
	for x > 0 && y > 0 {
		reverse = append(reverse, '=')
		x--
		y--
	}
	// Convert once to byte coordinates, coalescing nearby changes to avoid paying
	// another coordinate pair for a few unchanged characters inside a replacement.
	x, y = prefix, prefix
	base := string(a)
	at := len(string(a[:prefix]))
	var edits []edit
	for i := len(reverse) - 1; i >= 0; {
		if reverse[i] == '=' {
			at += utf8.RuneLen(a[x])
			x++
			y++
			i--
			continue
		}
		start, from := at, y
		for i >= 0 && reverse[i] != '=' {
			if reverse[i] == '-' {
				at += utf8.RuneLen(a[x])
				x++
			} else {
				y++
			}
			i--
		}
		e := edit{start: start, end: at, text: string(b[from:y])}
		if n := len(edits); n > 0 && start-edits[n-1].end <= 16 {
			last := &edits[n-1]
			last.text += base[last.end:start] + e.text
			last.end = at
		} else {
			edits = append(edits, e)
		}
	}
	return edits
}

func reconstruct(base, body string, edits []edit) (string, int, int) {
	var text strings.Builder
	at, repeated, lines := 0, 0, 0
	for _, e := range edits {
		if e.start < at || e.end < e.start || e.end > len(base) {
			return "", 0, 0
		}
		start := text.Len()
		text.WriteString(base[at:e.start])
		repeated += e.start - at
		lines += unchangedLines(body, start, text.Len())
		text.WriteString(e.text)
		at = e.end
	}
	start := text.Len()
	text.WriteString(base[at:])
	repeated += len(base) - at
	lines += unchangedLines(body, start, text.Len())
	return text.String(), repeated, lines
}

func unchangedLines(body string, start, end int) int {
	if end > len(body) || start == end {
		return 0
	}
	if start > 0 && body[start-1] != '\n' {
		n := strings.IndexByte(body[start:end], '\n')
		if n < 0 {
			return 0
		}
		start += n + 1
	}
	lines := strings.Count(body[start:end], "\n")
	if end == len(body) && end > start && body[end-1] != '\n' {
		lines++
	}
	return lines
}
