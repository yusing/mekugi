// Source: apps/mekugi/internal/router/app_server_composer_editor.go:72:153@[e9da429a] external-editor reconciliation.
package composer

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (e *Editor) ApplyExternal(text string) error {
	if text == e.Text {
		return nil
	}
	if !utf8.ValidString(text) {
		return errors.New("edited draft is not valid UTF-8")
	}
	var images []Token
	for _, image := range e.Images {
		label := e.Text[image.Start:image.End]
		if strings.Count(text, label) > 1 {
			return fmt.Errorf("attachment %s appears more than once", label)
		}
		if start := strings.Index(text, label); start >= 0 {
			image.Start, image.End = start, start+len(label)
			images = append(images, image)
		}
	}
	var selections []Token
	for _, selection := range e.Selections {
		label := e.Text[selection.Start:selection.End]
		if strings.Count(text, label) > 1 {
			return fmt.Errorf("selection %s appears more than once", label)
		}
		if start := strings.Index(text, label); start >= 0 {
			selection.Start, selection.End = start, start+len(label)
			selections = append(selections, selection)
		}
	}
	slices.SortFunc(selections, func(a, b Token) int { return a.Start - b.Start })
	var skills []Token
	for _, skill := range e.Skills {
		bindings := SkillBindings(text, skill.Name, skill.Path)
		if len(bindings) == 1 {
			skills = append(skills, bindings[0])
		}
	}
	var files []Token
	next := make(map[string]int)
	for _, file := range e.Files {
		label := e.Text[file.Start:file.End]
		oldBefore, _ := utf8.DecodeLastRuneInString(e.Text[:file.Start])
		oldAfter, _ := utf8.DecodeRuneInString(e.Text[file.End:])
		for offset := next[label]; offset < len(text); {
			index := strings.Index(text[offset:], label)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(label)
			offset = end
			before, _ := utf8.DecodeLastRuneInString(text[:start])
			after, _ := utf8.DecodeRuneInString(text[end:])
			// Existing adjacent text belongs outside the atomic token. Keep it
			// across unrelated editor changes, but do not bind a newly extended
			// filename merely because its prefix matches the old label.
			if start > 0 && !unicode.IsSpace(before) && (file.Start == 0 || before != oldBefore) ||
				end < len(text) && !unicode.IsSpace(after) && (file.End == len(e.Text) || after != oldAfter) {
				continue
			}
			file.Start, file.End = start, end
			next[label] = end
			files = append(files, file)
			break
		}
	}
	slices.SortFunc(files, func(a, b Token) int { return a.Start - b.Start })
	e.Record()
	e.Files = files
	e.Selections = selections
	e.Skills = skills
	e.Text, e.Images, e.CursorBack = text, images, 0
	e.RenumberImages()
	e.Changed()
	return nil
}
