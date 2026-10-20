// Source: apps/mekugi/internal/router/composer_selection.go:72:124@[e9da429a] quoted selection attachments.
package composer

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

const SelectionPrefix = "[Selected "

func SelectionLabel(label string) bool {
	return strings.HasPrefix(label, SelectionPrefix) && strings.HasSuffix(label, "]") && !strings.ContainsAny(label, "\n")
}

// selectionFrames quotes one mentioned selection. Content splits like file
// frames; nothing is dropped silently.
func (d Draft) selectionFrames(selection Token) []string {
	label := d.Text[selection.Start:selection.End]
	origin := ""
	if selection.Source != "" {
		origin = fmt.Sprintf(" from %q", selection.Source)
	}
	return frameText(selection.Text, func(start, end, total int) string {
		return fmt.Sprintf(attachments.SelectionPrefix+"%q%s (UTF-8 bytes %d:%d of %d; quoted from the user's screen, not a separate request):\n", label, origin, start, end, total)
	})
}

// RestoreSelections rebinds mentions from their submitted frames, so recalled
// input keeps its quotes after resume. Only a label that occurs once in the
// text binds; omitted content and ambiguous labels stay literal.
func RestoreSelections(text string, frames []string) []Token {
	var labels []string
	quotes := make(map[string]*Token)
	for _, frame := range frames {
		rest, ok := strings.CutPrefix(frame, attachments.SelectionPrefix)
		quoted, err := strconv.QuotedPrefix(rest)
		if !ok || err != nil {
			continue
		}
		label, _ := strconv.Unquote(quoted)
		rest = rest[len(quoted):]
		source := ""
		if after, ok := strings.CutPrefix(rest, " from "); ok {
			if quoted, err := strconv.QuotedPrefix(after); err == nil {
				source, _ = strconv.Unquote(quoted)
				rest = after[len(quoted):]
			}
		}
		header, body, ok := strings.Cut(rest, "\n")
		if !ok || !strings.HasPrefix(header, " (UTF-8 bytes ") || !SelectionLabel(label) {
			continue
		}
		if quotes[label] == nil {
			labels = append(labels, label)
			quotes[label] = &Token{Source: source}
		}
		quotes[label].Text += body
	}
	var selections []Token
	for _, label := range labels {
		if start := strings.Index(text, label); start >= 0 && strings.Count(text, label) == 1 {
			selection := *quotes[label]
			selection.Start, selection.End = start, start+len(label)
			selections = append(selections, selection)
		}
	}
	slices.SortFunc(selections, func(a, b Token) int { return a.Start - b.Start })
	return selections
}
