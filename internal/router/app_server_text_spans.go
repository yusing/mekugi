package router

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Codex TextElement ranges are relative to each text part, not the whole draft.
type composerByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type composerTextElement struct {
	ByteRange   composerByteRange `json:"byteRange"`
	Placeholder string            `json:"placeholder"`
}

func composerElementSpans(text string, elements []composerTextElement) []activityui.TextSpan {
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
		case composerSelectionLabel(label):
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
func restoredSkillBindings(text, name, path string) []composerSkill {
	var skills []composerSkill
	at := 0
	for word := range strings.FieldsSeq(text) {
		start := at + strings.Index(text[at:], word)
		if word == "$"+name {
			skills = append(skills, composerSkill{start, start + len(word), name, path})
		}
		at = start + len(word)
	}
	return skills
}

func unambiguousSkillBindings(text string, skills []composerSkill, spans []activityui.TextSpan) []composerSkill {
	paths := make(map[string]string)
	for _, skill := range skills {
		if previous, ok := paths[skill.name]; ok && previous != skill.path {
			paths[skill.name] = ""
		} else if !ok {
			paths[skill.name] = skill.path
		}
	}
	var bindings []composerSkill
	for name, path := range paths {
		if path == "" {
			continue
		}
		var explicit []activityui.TextSpan
		for _, span := range spans {
			if span.Kind == activityui.SkillToken && text[span.Start:span.End] == "$"+name {
				explicit = append(explicit, span)
			}
		}
		for _, binding := range restoredSkillBindings(text, name, path) {
			if len(explicit) == 0 || slices.ContainsFunc(explicit, func(span activityui.TextSpan) bool {
				return span.Start == binding.start && span.End == binding.end
			}) {
				bindings = append(bindings, binding)
			}
		}
	}
	slices.SortFunc(bindings, func(a, b composerSkill) int { return a.start - b.start })
	return bindings
}
