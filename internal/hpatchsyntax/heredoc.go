package hpatchsyntax

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// MaxHeredocBodyBytes bounds the decoded payload retained by one heredoc.
const MaxHeredocBodyBytes = 1 << 20

// PhysicalLine retains a script line's exact terminator separately from its text.
type PhysicalLine struct {
	Text       string
	Terminator string
}

// CommandFrame describes one inline command or complete heredoc command.
type CommandFrame struct {
	Marker    string
	Delimiter string
	StripTabs bool
	Body      string
	Next      int
}

// SplitPhysicalLines splits LF and CRLF scripts without losing body terminators.
func SplitPhysicalLines(source string) []PhysicalLine {
	raw := strings.Split(source, "\n")
	lines := make([]PhysicalLine, len(raw))
	for index, text := range raw {
		terminator := ""
		if index < len(raw)-1 {
			terminator = "\n"
			if trimmed, ok := strings.CutSuffix(text, "\r"); ok {
				text = trimmed
				terminator = "\r\n"
			}
		} else if trimmed, ok := strings.CutSuffix(text, "\r"); ok {
			text = trimmed
			terminator = "\r"
		}
		lines[index] = PhysicalLine{Text: text, Terminator: terminator}
	}
	return lines
}

// FrameCommand returns the complete physical-line span and decoded body for the
// command at headerIndex. Malformed frames retain their attributable lines so
// parsers and diagnostics do not reinterpret payload-shaped data as commands.
func FrameCommand(lines []PhysicalLine, headerIndex int, command string) (CommandFrame, error) {
	frame := CommandFrame{Next: headerIndex + 1}
	marker, err := heredocMarker(command)
	if err != nil {
		// An invalid heredoc header owns the remaining physical lines.
		frame.Next = len(lines)
		return frame, err
	}
	frame.Marker = marker
	if marker != "" {
		var ok bool
		frame.Delimiter, frame.StripTabs, ok = heredocDelimiter(marker)
		if !ok {
			frame.Next = len(lines)
			return frame, errors.New("invalid heredoc delimiter")
		}
		frame.Body, frame.Next, err = decodeHeredoc(lines, headerIndex, frame.Delimiter, frame.StripTabs)
		return frame, err
	}

	if !isInlineQuotedCommand(command) {
		return frame, nil
	}

	quoteOpen := scanQuotedOperand(command, false)
	if !quoteOpen {
		return frame, nil
	}
	for index := headerIndex + 1; index < len(lines); index++ {
		frame.Next = index + 1
		quoteOpen = scanQuotedOperand(lines[index].Text, quoteOpen)
		if !quoteOpen {
			break
		}
	}
	return frame, errors.New(`physical newline inside quoted operand; encode line terminators as \n or \r`)
}

func isInlineQuotedCommand(command string) bool {
	return strings.HasPrefix(command, "type ") ||
		strings.HasPrefix(command, "add ")
}

func scanQuotedOperand(text string, quoteOpen bool) bool {
	escaped := false
	for _, character := range text {
		if !quoteOpen {
			if character == '"' {
				quoteOpen = true
			}
			continue
		}
		switch {
		case escaped:
			escaped = false
		case character == '\\':
			escaped = true
		case character == '"':
			quoteOpen = false
		}
	}
	return quoteOpen
}

func heredocMarker(command string) (string, error) {
	operation, _, _ := strings.Cut(command, " ")
	if operation != "type" && operation != "add" && operation != "shell" {
		return "", nil
	}
	marker := unquotedDoubleLess(command)
	if marker < 0 {
		return "", nil
	}
	if marker > 0 && command[marker-1] == ' ' {
		return command[marker:], nil
	}
	return "", errors.New("invalid heredoc delimiter")
}

// heredocDelimiter uses the shell word parser without evaluating the result.
// Unlike redirect parsing, WordsSeq accepts literal parameter syntax in delimiters.
func heredocDelimiter(marker string) (delimiter string, stripTabs, ok bool) {
	if strings.ContainsAny(marker, "\x00\r\n") {
		return "", false, false
	}
	wordSource, found := strings.CutPrefix(marker, "<<")
	if !found {
		return "", false, false
	}
	wordSource, stripTabs = strings.CutPrefix(wordSource, "-")
	wordSource = strings.TrimLeft(wordSource, " \t")
	var word *syntax.Word
	for parsed, err := range syntax.NewParser().WordsSeq(strings.NewReader(wordSource)) {
		if err != nil || word != nil {
			return "", false, false
		}
		word = parsed
	}
	if word == nil || word.Pos().Offset() != 0 || strings.Trim(wordSource[word.End().Offset():], " \t") != "" {
		return "", false, false
	}
	var decoded strings.Builder
	if !heredocWord(&decoded, word.Parts, wordSource, false) {
		return "", false, false
	}
	delimiter = decoded.String()
	return delimiter, stripTabs, delimiter != "" && utf8.ValidString(delimiter) && !strings.ContainsAny(delimiter, "\r\n")
}

// Quote removal for a parsed redirection word, not shell evaluation.
// Source: mvdan.cc/sh/v3/syntax/parser.go:722:745@v3.14.1 unquotedWordPart
func heredocWord(out *strings.Builder, parts []syntax.WordPart, source string, quoted bool) bool {
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			for index := 0; index < len(part.Value); index++ {
				if part.Value[index] == '\\' && !quoted {
					index++
					if index == len(part.Value) {
						return false
					}
				}
				out.WriteByte(part.Value[index])
			}
		case *syntax.SglQuoted:
			out.WriteString(part.Value)
		case *syntax.DblQuoted:
			if !heredocWord(out, part.Parts, source, true) {
				return false
			}
		case *syntax.ParamExp:
			// Heredoc delimiters undergo quote removal, not parameter expansion.
			out.WriteString(source[part.Pos().Offset():part.End().Offset()])
		default:
			return false
		}
	}
	return true
}

func unquotedDoubleLess(text string) int {
	quoted := false
	escaped := false
	for index := 0; index < len(text); index++ {
		character := text[index]
		if !quoted {
			if character == '"' {
				quoted = true
				continue
			}
			if character == '<' && index+1 < len(text) && text[index+1] == '<' {
				return index
			}
			continue
		}
		switch {
		case escaped:
			escaped = false
		case character == '\\':
			escaped = true
		case character == '"':
			quoted = false
		}
	}
	return -1
}

func decodeHeredoc(lines []PhysicalLine, headerIndex int, delimiter string, stripTabs bool) (string, int, error) {
	nonDelimiter := "x"
	if delimiter == nonDelimiter {
		nonDelimiter = "y"
	}
	// Single-quoted chunks force literal parsing without evaluation-oriented
	// escapes. Prefix each row and the delimiter with ASCII: mvdan v3.14.1's
	// quoted-heredoc matcher otherwise miscounts a multibyte first character.
	// A dummy body row also makes Hdoc.End available for empty documents.
	quoted := "'_" + strings.ReplaceAll(delimiter, "'", "'\\''") + "'"
	header := ": <<" + quoted + "\n_" + nonDelimiter + "\n"
	bodyReader := &heredocReader{lines: lines[headerIndex+1:], stripTabs: stripTabs, nonDelimiter: nonDelimiter}
	bounded := &io.LimitedReader{R: bodyReader, N: int64(2*MaxHeredocBodyBytes + len(delimiter) + 4)}
	input := io.MultiReader(strings.NewReader(header), bounded)
	for stmt, parseErr := range syntax.NewParser().StmtsSeq(input) {
		if parseErr != nil {
			if bodyReader.invalidUTF8 {
				return "", len(lines), errors.New("heredoc body is not UTF-8")
			}
			if bounded.N == 0 {
				return "", len(lines), fmt.Errorf("heredoc body exceeds %d bytes", MaxHeredocBodyBytes)
			}
			return "", len(lines), fmt.Errorf("unterminated heredoc; expected closing delimiter %s: %w", delimiter, parseErr)
		}
		// Shell line 1 is the header and line 2 is synthetic. Remaining source
		// rows map directly to the original physical lines.
		end := headerIndex + int(stmt.Redirs[0].Hdoc.End().Line()) - 2
		var body strings.Builder
		for _, line := range lines[headerIndex+1 : end] {
			text := line.Text
			if stripTabs {
				text = strings.TrimLeft(text, "\t")
			}
			if len(text)+len(line.Terminator) > MaxHeredocBodyBytes-body.Len() {
				return "", end + 1, fmt.Errorf("heredoc body exceeds %d bytes", MaxHeredocBodyBytes)
			}
			body.WriteString(text)
			body.WriteString(line.Terminator)
		}
		if !utf8.ValidString(body.String()) {
			return "", end + 1, errors.New("heredoc body is not UTF-8")
		}
		return body.String(), end + 1, nil
	}
	return "", len(lines), fmt.Errorf("unterminated heredoc; expected closing delimiter %s", delimiter)
}

// heredocReader streams source rows without rebuilding the remaining script.
// Original bytes remain in PhysicalLine for extraction after shell framing.
type heredocReader struct {
	lines        []PhysicalLine
	pending      string
	nonDelimiter string
	stripTabs    bool
	invalidUTF8  bool
}

func (r *heredocReader) Read(buffer []byte) (int, error) {
	for r.pending == "" {
		if len(r.lines) == 0 {
			return 0, io.EOF
		}
		line := r.lines[0]
		r.lines = r.lines[1:]
		r.pending = line.Text
		if r.stripTabs {
			r.pending = strings.TrimLeft(r.pending, "\t")
		}
		r.invalidUTF8 = r.invalidUTF8 || !utf8.ValidString(r.pending)
		// The shell lexer ignores NUL. Shield such rows from delimiter matching;
		// their exact content is still extracted from the original lines.
		if strings.ContainsRune(r.pending, 0) {
			r.pending = r.nonDelimiter
		}
		r.pending = "_" + r.pending + line.Terminator
		if line.Terminator == "\r" {
			r.pending += "\n"
		}
	}
	count := copy(buffer, r.pending)
	r.pending = r.pending[count:]
	return count, nil
}
