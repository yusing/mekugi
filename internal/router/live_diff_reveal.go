package router

import (
	"math"
	"unicode/utf8"
)

// Edit previews reveal whole lines so a card redraws once per line rather
// than per character. The gate runs on received input, because the
// projection runs on that prefix.

// liveDiffPreviewPacer reveals bursty provider input at its recent average
// arrival rate, so a preview grows at a steady speed between bursts. A
// catch-up share bounds the lag, and a large backlog skips to its last
// window because the preview follows the tip.
type liveDiffPreviewPacer struct {
	shown, cursor, received int
	rate                    float64 // Bytes per frame, averaged over about eight frames.
	held                    int     // Frames the cursor has waited inside an unfinished line.
}

const (
	liveDiffPreviewMaxLag = 2 << 10
	// A unit that outlives about half a second is revealed as it streams, so
	// a long line or stalled stream still makes progress.
	liveDiffPreviewMaxHold = 15
)

// Encoded input is JSON or JavaScript source, where \n escapes a line break.
func (p *liveDiffPreviewPacer) advance(input string, finishing, encoded bool) int {
	p.rate += (float64(max(0, len(input)-p.received)) - p.rate) / 8
	p.received = len(input)
	p.shown = min(p.shown, len(input))
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
	if finishing && p.cursor == len(input) {
		p.shown, p.held = p.cursor, 0
	} else if boundary := liveDiffLineBoundary(input, p.shown, p.cursor, encoded); boundary > p.shown {
		p.shown, p.held = boundary, 0
	} else if p.cursor > p.shown {
		if p.held++; p.held > liveDiffPreviewMaxHold {
			p.shown = p.cursor
		}
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

// liveDiffLineBoundary returns the last line end in (from, to], or from.
func liveDiffLineBoundary(input string, from, to int, encoded bool) int {
	for end := to; end > from; end-- {
		if input[end-1] == '\n' {
			return end
		}
		if encoded && input[end-1] == 'n' && end >= 2 && input[end-2] == '\\' {
			slashes := 0
			for i := end - 2; i >= 0 && input[i] == '\\'; i-- {
				slashes++
			}
			if slashes%2 == 1 {
				return end
			}
		}
	}
	return from
}
