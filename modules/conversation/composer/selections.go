// Source: apps/mekugi/internal/router/composer_selection.go:72:124@[e9da429a] quoted selection attachments.
package composer

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

const SelectionPrefix = "[Selected "
const UserSelectionsPrefix = "\n\n---\nUser selections:\n"

func SelectionLabel(label string) bool {
	return strings.HasPrefix(label, SelectionPrefix) && strings.HasSuffix(label, "]") && !strings.ContainsAny(label, "\n")
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

// UserSelections appends supplied text as ordinary Markdown in the user's input.
func (d Draft) UserSelections() string {
	var text strings.Builder
	appendSelection := func(label, source, body string) {
		if text.Len() == 0 {
			text.WriteString(UserSelectionsPrefix)
		}
		fmt.Fprintf(&text, "- %s:", label)
		format, _ := sourcekind.Classify(source)
		if !strings.ContainsAny(body, "\r\n") && format.Language == "" && !strings.HasPrefix(strings.ToLower(label), "diff") {
			text.WriteString(" " + body + "\n")
			return
		}
		longest, run := 0, 0
		for _, character := range body {
			if character == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", max(3, longest+1))
		text.WriteString("\n  " + fence + format.Language + "\n  ")
		text.WriteString(strings.ReplaceAll(body, "\n", "\n  "))
		if !strings.HasSuffix(body, "\n") {
			text.WriteString("\n  ")
		}
		text.WriteString(fence + "\n")
	}
	for _, selection := range d.Selections {
		label := strings.TrimSuffix(strings.TrimPrefix(d.Text[selection.Start:selection.End], SelectionPrefix), "]")
		appendSelection(label, selection.Source, selection.Text)
	}
	for _, file := range d.Files {
		if file.Source == "upload" {
			appendSelection("Uploaded "+file.Name, "", file.Text)
		}
	}
	return strings.TrimSuffix(text.String(), "\n")
}
