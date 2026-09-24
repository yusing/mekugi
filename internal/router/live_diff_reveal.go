package router

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Streaming previews reveal whole units so a card redraws once per line or
// command rather than per character. Edit payloads are gated by line in the
// received input, because their projection runs on that prefix. Command and
// script text is gated on the displayed source, where shell quoting and the
// interpreter languages painted by liveDiffScriptSyntax are known.

// liveDiffPreviewPacer reveals bursty provider input at its recent average
// arrival rate, so a preview grows at a steady speed between bursts. A
// catch-up share bounds the lag, and a large backlog skips to its last
// window because the preview follows the tip.
type liveDiffPreviewPacer struct {
	shown, cursor, received int
	rate                    float64 // Bytes per frame, averaged over about eight frames.
	held                    int     // Frames the cursor has waited inside an unfinished line.
}

// liveDiffRevealUnits selects where the received-input reveal may stop.
type liveDiffRevealUnits struct {
	lines   bool // Stop only after a line break; otherwise follow the pace.
	encoded bool // Input is JSON or JavaScript source, where \n escapes a line break.
}

const (
	liveDiffPreviewMaxLag = 2 << 10
	// A unit that outlives about half a second is revealed as it streams, so
	// a long line or stalled stream still makes progress.
	liveDiffPreviewMaxHold = 15
)

func (p *liveDiffPreviewPacer) advance(input string, finishing bool, units liveDiffRevealUnits) int {
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
	if !units.lines || finishing && p.cursor == len(input) {
		p.shown, p.held = p.cursor, 0
	} else if boundary := liveDiffLineBoundary(input, p.shown, p.cursor, units.encoded); boundary > p.shown {
		p.shown, p.held = boundary, 0
	} else if p.cursor > p.shown {
		if p.held++; p.held > liveDiffPreviewMaxHold {
			p.shown = p.cursor
		}
	}
	if units.encoded && !(finishing && p.shown == len(input)) {
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

// liveDiffRevealGate buffers displayed script text to its last complete
// unit. It never retracts text already shown, and releases an unfinished
// unit after the same hold as the received-input pacer.
type liveDiffRevealGate struct {
	shown    string
	held     int
	released bool
	pending  bool // Hidden text still needs frames to count its hold.
}

func (g *liveDiffRevealGate) reveal(source string, spans []liveDiffSourceSpan, final bool) int {
	kept := 0
	if strings.HasPrefix(source, g.shown) {
		kept = len(g.shown)
	}
	n := len(source)
	switch end := liveDiffScriptBoundary(source, spans); {
	case final:
	case end > kept:
		g.held, g.released = 0, false
		n = end
	case g.released || len(source) == kept:
	default:
		if g.held++; g.held <= liveDiffPreviewMaxHold {
			n = kept
		} else {
			g.released = true
		}
	}
	g.shown, g.pending = source[:n], n < len(source)
	return n
}

// liveDiffScriptBoundary returns the end of the last complete unit in a
// displayed script. Shell regions end units at list operators (;, &, &&, ||)
// and line breaks, keeping a pipeline whole; interpreter source ends them at
// line breaks and statement semicolons. Quotes, substitutions, and comments
// hide operators. Display cadence only: nothing here selects what runs.
func liveDiffScriptBoundary(source string, spans []liveDiffSourceSpan) int {
	last := 0
	mark := func(end int) { last = max(last, end) }
	if len(spans) == 0 {
		spans = []liveDiffSourceSpan{{Path: "stream.sh"}}
	}
	for i, span := range spans {
		start, end := min(max(0, span.Offset), len(source)), len(source)
		if i+1 < len(spans) {
			end = min(max(start, spans[i+1].Offset), len(source))
		}
		if span.Path == "stream.sh" {
			liveDiffShellUnits(source[start:end], start, mark)
		} else {
			liveDiffStatementUnits(source[start:end], start, 0, mark)
		}
	}
	return last
}

// liveDiffStatementUnits marks line breaks and semicolons outside string
// literals. Enclosing is the shell quote around inline source, which cannot
// open a string inside it. A line break ends a unit even inside a multiline
// string, and resets quotes that cannot span lines, such as an apostrophe in
// a comment.
func liveDiffStatementUnits(source string, base int, enclosing byte, mark func(int)) {
	var quote byte
	for i := 0; i < len(source); i++ {
		switch c := source[i]; {
		case c == '\\':
			i++
		case c == '\n':
			mark(base + i + 1)
			if quote != '`' {
				quote = 0
			}
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			if c != enclosing {
				quote = c
			}
		case c == ';':
			mark(base + i + 1)
		}
	}
}

type liveDiffHeredoc struct {
	delimiter string
	strip     bool // <<- removes leading tabs before matching the delimiter.
	script    bool // The command reads the body as interpreter source.
}

// liveDiffShellUnits lexes possibly unfinished shell text. It recognizes the
// same literal interpreter source as shellInterpreterScriptProjection — a
// source flag's quoted argument or a heredoc body — and gates it by statement.
func liveDiffShellUnits(source string, base int, mark func(int)) {
	var (
		command   []string
		word      strings.Builder
		inWord    bool
		sourceArg bool // The next word is literal interpreter source.
		heredocs  []liveDiffHeredoc
	)
	interpreter := func() string {
		args := command
		if len(args) >= 2 && shellsyntax.InterpreterIdentity(args[0]) == "uv" && args[1] == "run" {
			args = args[2:]
		}
		if len(args) == 0 {
			return ""
		}
		if name := shellsyntax.InterpreterIdentity(args[0]); shellInterpreterPattern.MatchString(name) {
			return name
		}
		return ""
	}
	endWord := func() {
		if !inWord {
			return
		}
		text := word.String()
		word.Reset()
		inWord, sourceArg = false, false
		command = append(command, text)
		if name := interpreter(); name != "" && len(command) > 1 {
			sourceArg, _ = shellInterpreterFlag(name, text)
		}
	}
	endCommand := func() {
		endWord()
		command, sourceArg = nil, false
	}
	for i := 0; i < len(source); {
		c := source[i]
		next := byte(0)
		if i+1 < len(source) {
			next = source[i+1]
		}
		switch {
		case c == '\\' && next == '\n':
			i += 2 // A continued line is one unit.
		case c == '\\':
			word.WriteString(source[i:min(i+2, len(source))])
			inWord = true
			i += 2
		case c == '\'' || c == '"' || c == '$' && next == '\'':
			open := i
			if c == '$' {
				open++
			}
			closing := liveDiffShellQuoteEnd(source, open)
			if sourceArg && !inWord {
				liveDiffStatementUnits(source[open+1:closing], base+open+1, source[open], mark)
			}
			word.WriteString(source[open+1 : closing])
			inWord = true
			i = min(closing+1, len(source))
		case c == '`':
			closing := liveDiffShellQuoteEnd(source, i)
			word.WriteString(source[i:closing])
			inWord = true
			i = min(closing+1, len(source))
		case c == '$' && next == '(':
			closing := liveDiffShellParenEnd(source, i+1)
			word.WriteString(source[i:closing])
			inWord = true
			i = closing
		case c == '#' && !inWord:
			if end := strings.IndexByte(source[i:], '\n'); end >= 0 {
				i += end
			} else {
				i = len(source)
			}
		case c == '\n':
			endCommand()
			i++
			mark(base + i)
			for _, doc := range heredocs {
				i = doc.units(source, i, base, mark)
			}
			heredocs = nil
		case c == ';':
			endCommand()
			i++
			if next == ';' || next == '&' {
				i++
			}
			mark(base + i)
		case c == '&' && next == '&', c == '|' && next == '|':
			endCommand()
			i += 2
			mark(base + i)
		case c == '&' && i+1 == len(source):
			i++ // Still arriving: it may become && or &>.
		case c == '&' && next != '>' && (i == 0 || source[i-1] != '>' && source[i-1] != '<'):
			endCommand()
			i++
			mark(base + i)
		case c == '|':
			endCommand() // A pipeline stage starts a command but not a unit.
			i++
			if next == '&' {
				i++
			}
		case c == '<' && next == '<' && !strings.HasPrefix(source[i:], "<<<"):
			endWord()
			doc := liveDiffHeredoc{script: interpreter() != ""}
			i += 2
			if i < len(source) && source[i] == '-' {
				doc.strip = true
				i++
			}
			for i < len(source) && (source[i] == ' ' || source[i] == '\t') {
				i++
			}
			var delimiter strings.Builder
			for ; i < len(source) && !strings.ContainsRune(" \t\n;&|<>()", rune(source[i])); i++ {
				if source[i] != '\'' && source[i] != '"' && source[i] != '\\' {
					delimiter.WriteByte(source[i])
				}
			}
			if doc.delimiter = delimiter.String(); doc.delimiter != "" {
				heredocs = append(heredocs, doc)
			}
		case c == ' ' || c == '\t' || c == '<' || c == '>':
			endWord()
			i++
		case c == '(' || c == ')':
			endCommand()
			i++
		default:
			word.WriteByte(c)
			inWord = true
			i++
		}
	}
}

// units marks each body line and returns the offset after the delimiter line.
func (doc liveDiffHeredoc) units(source string, at, base int, mark func(int)) int {
	for at < len(source) {
		end := strings.IndexByte(source[at:], '\n')
		if end < 0 {
			if doc.script {
				liveDiffStatementUnits(source[at:], base+at, 0, mark)
			}
			return len(source)
		}
		line, next := source[at:at+end], at+end+1
		if doc.strip {
			line = strings.TrimLeft(line, "\t")
		}
		if doc.script && line != doc.delimiter {
			liveDiffStatementUnits(line, base+at, 0, mark)
		}
		mark(base + next)
		at = next
		if line == doc.delimiter {
			break
		}
	}
	return at
}

// liveDiffShellQuoteEnd returns the closing quote's index, or len(source)
// while it is still arriving. Single quotes have no escapes.
func liveDiffShellQuoteEnd(source string, open int) int {
	quote := source[open]
	escapes := quote != '\'' || open > 0 && source[open-1] == '$'
	for i := open + 1; i < len(source); i++ {
		if escapes && source[i] == '\\' {
			i++
		} else if source[i] == quote {
			return i
		}
	}
	return len(source)
}

// liveDiffShellParenEnd returns the offset after the ( at open's match.
func liveDiffShellParenEnd(source string, open int) int {
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '\\':
			i++
		case '\'', '"', '`':
			i = liveDiffShellQuoteEnd(source, i)
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return len(source)
}
