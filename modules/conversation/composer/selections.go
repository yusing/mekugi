// Source: apps/mekugi/internal/router/composer_selection.go:72:124@[e9da429a] quoted selection attachments.
package composer

import (
	"slices"
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
	frames := frameText(selection.Text, func(_, _, _ int) string { return "" })
	for i, body := range frames {
		frames[i] = attachments.FormatSelection(label, selection.Source, body)
	}
	return frames
}

// RestoreSelections rebinds mentions from their submitted frames, so recalled
// input keeps its quotes after resume. Only a label that occurs once in the
// text binds; omitted content and ambiguous labels stay literal.
func RestoreSelections(text string, frames []string) []Token {
	var labels []string
	quotes := make(map[string]*Token)
	for _, frame := range frames {
		selection, ok := attachments.ParseSelection(frame)
		if !ok || !SelectionLabel(selection.Label) {
			continue
		}
		label, source, body := selection.Label, selection.Source, selection.Body
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
