package router

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func composerContentSkills(content []composerUserContent) []composer.Token {
	var skills []composer.Token
	for _, part := range content {
		if part.Type == "skill" && part.Path != "" {
			skills = append(skills, composer.Token{Name: part.Name, Path: part.Path})
		}
		if part.Type == "text" {
			frames, _ := attachments.Decode(part.Text)
			for _, frame := range frames {
				if name, path, _ := attachments.ParseSkillFrame(frame); name != "" {
					skills = append(skills, composer.Token{Name: name, Path: path})
				}
			}
		}
	}
	return skills
}

type composerUserContent struct {
	Type     string                     `json:"type"`
	Text     string                     `json:"text"`
	Name     string                     `json:"name"`
	Path     string                     `json:"path"`
	Elements []binding.InputTextElement `json:"text_elements"`
}

func composerElementSpans(text string, elements []binding.InputTextElement) []activityui.TextSpan {
	var spans []activityui.TextSpan
	for _, element := range elements {
		start, end := element.ByteRange.Start, element.ByteRange.End
		if start < 0 || end <= start || end > len(text) || !utf8.ValidString(text[:start]) || !utf8.ValidString(text[start:end]) {
			continue
		}
		label := text[start:end]
		if element.Placeholder != label {
			continue
		}
		kind := activityui.FileToken
		switch {
		case strings.HasPrefix(label, "@"):
		case strings.HasPrefix(label, "$"):
			kind = activityui.SkillToken
		case composer.SelectionLabel(label):
			kind = activityui.SelectionToken
		default:
			continue
		}
		spans = append(spans, activityui.TextSpan{Start: start, End: end, Kind: kind})
	}
	slices.SortFunc(spans, func(a, b activityui.TextSpan) int { return cmp.Compare(a.Start, b.Start) })
	end := 0
	return slices.DeleteFunc(spans, func(span activityui.TextSpan) bool {
		if span.Start < end {
			return true
		}
		end = span.End
		return false
	})
}

// Rebind only complete, unambiguous skill tokens from authoritative history or
// the edited draft. A similarly prefixed word must not activate a stale skill.

func unambiguousSkillBindings(text string, skills []composer.Token, spans []activityui.TextSpan) []composer.Token {
	paths := make(map[string]string)
	ambiguous := make(map[string]bool)
	for _, skill := range skills {
		if previous, ok := paths[skill.Name]; ok && previous != skill.Path {
			ambiguous[skill.Name] = true
		} else if !ok {
			paths[skill.Name] = skill.Path
		}
	}
	var bindings []composer.Token
	for name, path := range paths {
		if ambiguous[name] {
			continue
		}
		var explicit []activityui.TextSpan
		for _, span := range spans {
			if span.Kind == activityui.SkillToken && text[span.Start:span.End] == "$"+name {
				explicit = append(explicit, span)
			}
		}
		for _, binding := range composer.SkillBindings(text, name, path) {
			if len(explicit) == 0 || slices.ContainsFunc(explicit, func(span activityui.TextSpan) bool {
				return span.Start == binding.Start && span.End == binding.End
			}) {
				bindings = append(bindings, binding)
			}
		}
	}
	slices.SortFunc(bindings, func(a, b composer.Token) int { return a.Start - b.Start })
	return bindings
}
