package livediff

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi"
)

// SequenceMatcher has quadratic worst-case cost. Word emphasis is optional;
// keep large blocks at their ordinary row fill instead of delaying a frame.
const maxWordDiffTokens = 2048

type wordSpan struct{ start, end int }

func colorHunkWords(ctx context.Context, theme Theme, rows []mekugi.ReviewRow, before, after []string) ([]string, []string, error) {
	// ColorSource returns cached slices. Decoration must not mutate that cache.
	before, after = slices.Clone(before), slices.Clone(after)
	oldIndex, newIndex := 0, 0
	for i := 0; i < len(rows); {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if rows[i].Kind == ' ' {
			oldIndex++
			newIndex++
			i++
			continue
		}
		oldStart, newStart := oldIndex, newIndex
		var oldSource, newSource strings.Builder
		for i < len(rows) && rows[i].Kind != ' ' {
			row := rows[i]
			text := Safe(strings.TrimSuffix(row.Text, "\n"), false) + "\n"
			if row.Kind == '-' {
				oldSource.WriteString(text)
				oldIndex++
			} else {
				newSource.WriteString(text)
				newIndex++
			}
			i++
		}
		if oldStart == oldIndex || newStart == newIndex || oldSource.Len()+newSource.Len() > MaxSyntaxBytes {
			continue
		}
		oldTokens, nextTokens := wordTokens(oldSource.String()), wordTokens(newSource.String())
		if oldTokens == nil || nextTokens == nil || len(oldTokens)+len(nextTokens) > maxWordDiffTokens {
			continue
		}
		matcher := difflib.NewMatcherWithJunk(oldTokens, nextTokens, false, func(token string) bool {
			return strings.TrimSpace(token) == ""
		})
		ops := matcher.GetOpCodes()
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		colorWordSide(theme, '-', before[oldStart:oldIndex], wordChanges(oldTokens, ops, true))
		colorWordSide(theme, '+', after[newStart:newIndex], wordChanges(nextTokens, ops, false))
	}
	return before, after, nil
}

// Keep identifiers and prose words whole, including Unicode combining marks.
// Graphemes are indivisible: an SGR inside an emoji changes terminal geometry.
// Punctuation is separate so a changed operator need not emphasize its operands.
func wordTokens(source string) []string {
	var tokens []string
	start, previous := 0, -1
	for clusters := uniseg.NewGraphemes(source); clusters.Next(); {
		at, _ := clusters.Positions()
		r, _ := utf8.DecodeRuneInString(clusters.Str())
		class := 0
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_' {
			class = 1
		} else if unicode.IsSpace(r) {
			class = 2
		}
		if at > start && (class != previous || class == 0) {
			tokens = append(tokens, source[start:at])
			if len(tokens) > maxWordDiffTokens {
				return nil
			}
			start = at
		}
		previous = class
	}
	return append(tokens, source[start:])
}

func wordChanges(tokens []string, ops []difflib.OpCode, before bool) []wordSpan {
	positions := make([]int, len(tokens)+1)
	for i, token := range tokens {
		positions[i+1] = positions[i] + len(token)
	}
	var spans []wordSpan
	for _, op := range ops {
		start, end := op.J1, op.J2
		if before {
			start, end = op.I1, op.I2
		}
		if op.Tag != 'e' && start != end {
			spans = append(spans, wordSpan{positions[start], positions[end]})
		}
	}
	return spans
}

// The input contains only safe text and our generated foreground SGRs. Track
// source-byte offsets through those SGRs to add backgrounds without losing syntax.
func colorWordSide(theme Theme, kind byte, lines []string, spans []wordSpan) {
	position, span := 0, 0
	for i, line := range lines {
		var out strings.Builder
		active := false
		for at := 0; at < len(line); {
			if strings.HasPrefix(line[at:], "\x1b[") {
				end := strings.IndexByte(line[at:], 'm') + at + 1
				out.WriteString(line[at:end])
				at = end
				continue
			}
			for span < len(spans) && position >= spans[span].end {
				span++
			}
			changed := span < len(spans) && position >= spans[span].start
			if changed != active {
				if changed {
					out.WriteString(theme.WordBackground(kind))
				} else {
					out.WriteString(theme.RowBackground(kind))
				}
				active = changed
			}
			out.WriteByte(line[at])
			position++
			at++
		}
		if active {
			out.WriteString(theme.RowBackground(kind))
		}
		lines[i] = out.String()
		position++ // The source newline separates rows but is not painted.
	}
}
