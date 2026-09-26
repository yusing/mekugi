package router

import (
	"math"
	"unicode/utf8"
)

// Edit previews reveal whole lines so a card redraws once per line rather
// than per character. The gate runs on received input, because the
// projection runs on that prefix.

// liveDiffPreviewPacer's cursor follows bursty provider input at its recent
// average arrival rate. The shown prefix trails the cursor by one complete
// source unit per frame, so a burst's statements stay distinct. A large backlog
// skips to its last window because the preview follows the tip.
type liveDiffPreviewPacer struct {
	shown, cursor, received int
	rate                    float64 // Bytes per frame, averaged over about eight frames.
}

const liveDiffPreviewMaxLag = 2 << 10

// Encoded input is JSON or JavaScript source, where \n escapes a line break.
func (p *liveDiffPreviewPacer) advance(input string, finishing, encoded bool) int {
	p.rate += (float64(max(0, len(input)-p.received)) - p.rate) / 8
	p.received = len(input)
	p.shown = min(p.shown, len(input))
	if len(input)-p.shown > liveDiffPreviewMaxLag {
		from := len(input) - liveDiffPreviewMaxLag
		if boundary := liveDiffLineBoundary(input, from, len(input), encoded); boundary > from {
			p.shown = boundary
		}
	}
	p.cursor = max(min(p.cursor, len(input)), len(input)-liveDiffPreviewMaxLag)
	backlog := len(input) - p.cursor
	share := 8
	if finishing {
		share = 3 // The call is complete; converge on its final input promptly.
	}
	p.cursor += max(int(math.Ceil(p.rate)), (backlog+share-1)/share, 4)
	if p.cursor >= len(input) {
		p.cursor = len(input)
	}
	for p.cursor < len(input) && !utf8.RuneStart(input[p.cursor]) {
		p.cursor++
	}
	if finishing && p.cursor == len(input) && len(input) > liveDiffPreviewMaxLag {
		// Retain the bounded catch-up policy for oversized input. Ordinary
		// bursts still reveal every queued statement even after completion.
		p.shown = p.cursor
	} else if boundary := liveDiffLineBoundary(input, p.shown, p.cursor, encoded); boundary > p.shown {
		p.shown = boundary
	} else if finishing && p.cursor == len(input) {
		p.shown = p.cursor
	}

	if encoded && !(finishing && p.shown == len(input)) {
		p.shown = liveDiffEscapeEnd(input, p.shown)
	}
	return p.shown
}

// liveDiffEscapeEnd backs a cut in encoded source off an unfinished escape.
// A decoder reading the prefix would otherwise see a stray backslash, which
// can end a string early and make a shell call vanish for a frame.
func liveDiffEscapeEnd(input string, n int) int {
	for at := n - 1; at >= max(0, n-6); at-- {
		if input[at] != '\\' {
			continue
		}
		run := 0
		for i := at; i >= 0 && input[i] == '\\'; i-- {
			run++
		}
		if run%2 == 0 {
			return n // The backslash nearest the cut is itself escaped.
		}
		digits := 0
		if at+1 < n {
			switch input[at+1] {
			case 'u':
				digits = 4
			case 'x':
				digits = 2
			}
		}
		if at+1 == n || n-(at+2) < digits {
			return at
		}
		return n
	}
	return n
}

// liveDiffLineBoundary returns the next candidate source-unit end. Semicolons
// are only candidates: decoded target syntax rejects strings and comments.
func liveDiffLineBoundary(input string, from, to int, encoded bool) int {
	for end := from + 1; end <= to; end++ {
		if input[end-1] == '\n' || input[end-1] == ';' {
			return end
		}
		if encoded && input[end-1] == 'n' && end >= 2 && input[end-2] == '\\' {
			// Nested interpreter literals have another escape layer. Decode
			// and validate in projection; raw slash parity cannot tell whether
			// this is a target newline or merely quoted transport text.
			return end
		}
	}
	return from
}
