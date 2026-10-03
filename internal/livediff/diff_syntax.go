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
	return r.ColorDiffPath(ctx, theme, "", source)
}

// ColorDiffPath supplies the file identity for host hunks without unified
// headers. Explicit headers still select the language for each file.
func (r *Renderer) ColorDiffPath(ctx context.Context, theme Theme, path, source string) ([]string, error) {
	rows, err := r.ColorDiffRows(ctx, theme, path, source)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(rows))
	for i, row := range rows {
		if row.Kind == 0 {
			lines[i] = row.Text
			if unifiedHunk.MatchString(row.Text) {
				lines[i] = Subtle + row.Text + SubtleReset
			}
		} else {
			lines[i] = SourceLine(theme, ansi.StringWidth(row.Text)+4, "", row.Text, row.Kind)
		}
	}
	return lines, nil
}

// DiffRows parses display-only unified output, including clipped and partial
// hunks. Metadata has no source-row kind; source rows omit their diff marker.
func DiffRows(source string) []mekugi.ReviewRow {
	rows, _ := diffRows(source, "", nil)
	return rows
}

// ColorDiffRows uses the same row geometry as DiffRows and the pane's source
// lexer. Callers can wrap and fill rows at their own viewport width.
func (r *Renderer) ColorDiffRows(ctx context.Context, theme Theme, path, source string) ([]mekugi.ReviewRow, error) {
	return diffRows(source, path, func(review mekugi.ReviewFile, rows []mekugi.ReviewRow) error {
		before, after, err := r.ColorHunk(ctx, theme, review, rows)
		if err != nil {
			return err
		}
		AlignHunkColors(rows, before, after)
		return nil
	})
}

func diffRows(source, path string, color func(mekugi.ReviewFile, []mekugi.ReviewRow) error) ([]mekugi.ReviewRow, error) {
	lines := strings.Split(source, "\n")
	result := make([]mekugi.ReviewRow, len(lines))
	for i, line := range lines {
		result[i].Text = line
	}
	review := mekugi.ReviewFile{BeforePath: path, AfterPath: path}
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
			review.BeforePath = unifiedPath(line[4:])
			review.AfterPath = unifiedPath(lines[i+1][4:])
			i += 2
			continue
		}
		// A clipped tail can start with the second file header.
		if strings.HasPrefix(line, "+++ ") && (strings.HasPrefix(strings.TrimSpace(line[4:]), "\"") || i+1 < len(lines) && unifiedHunk.MatchString(lines[i+1])) {
			review.AfterPath = unifiedPath(line[4:])
			i++
			continue
		}
		match := unifiedHunk.FindStringSubmatch(line)
		if match == nil {
			if len(line) > 0 && strings.ContainsRune(" +-", rune(line[0])) {
				result[i] = mekugi.ReviewRow{Kind: line[0], Text: line[1:]}
			}
			i++
			continue
		}
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
			// Retained output can trim a blank context row to empty.
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
		if color != nil && len(source) <= MaxSyntaxBytes {
			if err := color(review, rows); err != nil {
				return nil, err
			}
		}
		for j, row := range rows {
			if lines[indexes[j]] != "" {
				result[indexes[j]] = row
			}
		}
	}
	return result, nil
}

// AlignHunkColors replaces caller-owned row text with its before/after color,
// preserving hunk geometry without allocating another row slice.
func AlignHunkColors(rows []mekugi.ReviewRow, before, after []string) {
	oldIndex, newIndex := 0, 0
	for i, row := range rows {
		if row.Kind != '+' {
			rows[i].Text = before[oldIndex]
			oldIndex++
		}
		if row.Kind != '-' {
			rows[i].Text = after[newIndex]
			newIndex++
		}
	}
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
