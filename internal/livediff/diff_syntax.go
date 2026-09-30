package livediff

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

var unifiedHunk = regexp.MustCompile(`^@@ -[0-9]+(?:,([0-9]+))? \+[0-9]+(?:,([0-9]+))? @@`)

// ColorDiff decorates unified output without changing its rows or metadata.
// Partial streaming hunks use the same source lexer as the diff pane.
func (r *Renderer) ColorDiff(ctx context.Context, theme Theme, source string) ([]string, error) {
	lines := strings.Split(source, "\n")
	if len(source) > MaxSyntaxBytes {
		return lines, nil
	}
	review := mekugi.ReviewFile{}
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
			review.BeforePath = unifiedPath(line[4:])
			review.AfterPath = unifiedPath(lines[i+1][4:])
			i += 2
			continue
		}
		match := unifiedHunk.FindStringSubmatch(line)
		if match == nil {
			if len(line) > 0 && (line[0] == '+' || line[0] == '-') {
				lines[i] = SourceLine(theme, ansi.StringWidth(line)+3, "", line[1:], line[0])
			}
			i++
			continue
		}
		lines[i] = Subtle + line + SubtleReset
		count := func(s string) int {
			if s == "" {
				return 1
			}
			n, _ := strconv.Atoi(s)
			return n
		}
		oldLeft, newLeft := count(match[1]), count(match[2])
		i++
		var rows []mekugi.ReviewRow
		var indexes []int
		for i < len(lines) && (oldLeft > 0 || newLeft > 0) {
			text := lines[i]
			if strings.HasPrefix(text, "\\ No newline at end of file") {
				i++
				continue
			}
			// Retained output trims the single space of a blank context row.
			if text == "" {
				text = " "
			}
			if !strings.ContainsRune(" +-", rune(text[0])) {
				break
			}
			kind := text[0]
			if kind != '+' {
				oldLeft--
			}
			if kind != '-' {
				newLeft--
			}
			rows = append(rows, mekugi.ReviewRow{Kind: kind, Text: text[1:]})
			indexes = append(indexes, i)
			i++
		}
		before, after, err := r.ColorHunk(ctx, theme, review, rows)
		if err != nil {
			return nil, err
		}
		oldIndex, newIndex := 0, 0
		for j, row := range rows {
			text := ""
			if row.Kind != '+' {
				text = before[oldIndex]
				oldIndex++
			}
			if row.Kind != '-' {
				text = after[newIndex]
				newIndex++
			}
			if lines[indexes[j]] != "" {
				lines[indexes[j]] = SourceLine(theme, ansi.StringWidth(text)+4, "", text, row.Kind)
			}
		}
	}
	return lines, nil
}

// Output retention expands the separator tab in diff -u timestamp headers.
var unifiedTimestamp = regexp.MustCompile(` {4}[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?: [+-][0-9]{4})?$`)

func unifiedPath(path string) string {
	path, _, _ = strings.Cut(path, "\t")
	path = unifiedTimestamp.ReplaceAllString(path, "")
	if unquoted, err := strconv.Unquote(path); err == nil {
		path = unquoted
	}
	if path == "/dev/null" {
		return ""
	}
	return path
}
