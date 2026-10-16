package diffrender

// Source: internal/livediff/diff_syntax.go@[b95dde39]
import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
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
func DiffRows(source string) []diffmodel.ReviewRow {
	rows, _ := diffRows(source, "", nil, nil)
	return rows
}

// ColorDiffRows uses the same row geometry as DiffRows and the pane's source
// lexer. Callers can wrap and fill rows at their own viewport width.
func (r *Renderer) ColorDiffRows(ctx context.Context, theme Theme, path, source string) ([]diffmodel.ReviewRow, error) {
	return diffRows(source, path, nil, func(review diffmodel.ReviewFile, rows []diffmodel.ReviewRow) error {
		before, after, err := r.ColorHunk(ctx, theme, review, rows)
		if err != nil {
			return err
		}
		diffmodel.AlignRows(rows, before, after)
		return nil
	})
}

type diffSection struct {
	start  int
	hunks  int
	review diffmodel.ReviewFile
}

// DiffOutput replaces file-header pairs with a separator and the shared file
// heading. Row positions stay stable for retained tails and source copying.
// An open stream cannot establish the last file's complete changed-line counts.
func (r *Renderer) DiffOutput(ctx context.Context, theme Theme, source string, complete bool) ([]string, error) {
	var sections []diffSection
	var color func(diffmodel.ReviewFile, []diffmodel.ReviewRow) error
	if !r.LayoutOnly {
		color = func(review diffmodel.ReviewFile, rows []diffmodel.ReviewRow) error {
			before, after, err := r.ColorHunk(ctx, theme, review, rows)
			if err == nil {
				diffmodel.AlignRows(rows, before, after)
			}
			return err
		}
	}
	rows, err := diffRows(source, "", &sections, color)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = row.Text
		if row.Kind != 0 {
			if r.LayoutOnly {
				lines[i] = string(row.Kind) + row.Text
			} else {
				lines[i] = SourceLine(theme, ansi.StringWidth(row.Text)+4, "", row.Text, row.Kind)
			}
		} else if unifiedHunk.MatchString(row.Text) && !r.LayoutOnly {
			lines[i] = Subtle + row.Text + SubtleReset
		}
	}
	for i, section := range sections {
		review := section.review
		if section.hunks == 0 || !complete && i == len(sections)-1 {
			review.Incomplete = "partial output"
		}
		before, after := review.BeforePath, review.AfterPath
		// Paired Git prefixes identify the same workspace-relative names.
		if strings.HasPrefix(before, "a/") && strings.HasPrefix(after, "b/") {
			before, after = before[2:], after[2:]
		}
		label := "M " + after
		switch {
		case before == "":
			label = "A " + after
		case after == "":
			label = "D " + before
		case before != after:
			label = "R " + pathdisplay.Move("", before, after)
		}
		added, removed := review.LineCounts()
		stats := fmt.Sprintf(" +%d -%d", added, removed)
		if added < 0 || removed < 0 {
			stats = " counts unavailable"
		}
		lines[section.start] = ""
		lines[section.start+1] = Header(label, ansi.StringWidth(textsanitize.Safe(label, false)+stats), diffmodel.Counts{added, removed}, theme)
		if r.LayoutOnly {
			lines[section.start+1] = ansi.Strip(lines[section.start+1])
		}
	}
	return lines, nil
}

func diffRows(source, path string, sections *[]diffSection, color func(diffmodel.ReviewFile, []diffmodel.ReviewRow) error) ([]diffmodel.ReviewRow, error) {
	lines := strings.Split(source, "\n")
	result := make([]diffmodel.ReviewRow, len(lines))
	for i, line := range lines {
		result[i].Text = line
	}
	section := -1
	finish := func(end int) {
		if section >= 0 {
			part := &(*sections)[section]
			part.review.Diff = strings.Join(lines[part.start:end], "\n")
		}
	}
	review := diffmodel.ReviewFile{BeforePath: path, AfterPath: path}
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(line, "diff --git ") {
			finish(i)
			section = -1
		}
		if strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
			finish(i)
			review.BeforePath = unifiedPath(line[4:])
			review.AfterPath = unifiedPath(lines[i+1][4:])
			if sections != nil {
				*sections = append(*sections, diffSection{start: i, review: review})
				section = len(*sections) - 1
			}
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
			if section >= 0 && line != "" && !strings.HasPrefix(line, "\\ No newline at end of file") {
				(*sections)[section].review.Incomplete = "unrecognized output"
			}
			if len(line) > 0 && strings.ContainsRune(" +-", rune(line[0])) {
				result[i] = diffmodel.ReviewRow{Kind: line[0], Text: line[1:]}
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
		var rows []diffmodel.ReviewRow
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
			rows = append(rows, diffmodel.ReviewRow{Kind: kind, Text: text[1:]})
			indexes = append(indexes, i)
			i++
		}
		if section >= 0 {
			part := &(*sections)[section]
			part.hunks++
			if oldLeft != 0 || newLeft != 0 {
				part.review.Incomplete = "partial hunk"
			}
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
	finish(len(lines))
	return result, nil
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
