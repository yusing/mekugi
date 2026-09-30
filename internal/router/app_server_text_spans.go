package router

import (
	"bytes"
	"cmp"
	"encoding/xml"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Managed skills have a name, but no Codex filesystem path. Their instructions
// live in the same durable attachment envelope as file snapshots.
type composerSkillReference struct {
	XMLName xml.Name `xml:"skill"`
	Name    string   `xml:"name,attr"`
}

func managedSkillReference(name string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(name))
	return `<skill name="` + escaped.String() + `"/>`
}

func selectedSkillSource(text string) (name, path string) {
	if name, path = selectedSkillInstructions(text); name != "" {
		return name, path
	}
	text = strings.TrimSpace(text)
	var reference composerSkillReference
	if xml.Unmarshal([]byte(text), &reference) != nil || reference.Name == "" || text != managedSkillReference(reference.Name) {
		return "", ""
	}
	return reference.Name, ""
}

func managedSkillFrame(frame string) bool {
	if frame == "Attached skill references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced skills separately if needed." {
		return true
	}
	if name, _, _ := skillAttachmentFrame(frame); name != "" {
		return true
	}
	return false
}

func skillAttachmentFrame(frame string) (name, path, rest string) {
	rest, ok := strings.CutPrefix(frame, "Attached skill ")
	if !ok {
		return "", "", ""
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", "", ""
	}
	name, _ = strconv.Unquote(quoted)
	rest = rest[len(quoted):]
	if source, ok := strings.CutPrefix(rest, " from "); ok {
		quoted, err = strconv.QuotedPrefix(source)
		if err != nil {
			return "", "", ""
		}
		path, _ = strconv.Unquote(quoted)
		rest = source[len(quoted):]
	}
	if !strings.HasPrefix(rest, " (UTF-8 bytes ") && !strings.HasPrefix(rest, ": CONTENT NOT ATTACHED (") {
		return "", "", ""
	}
	return name, path, rest
}

func composerContentSkills(content []composerUserContent) []composerSkill {
	var skills []composerSkill
	for _, part := range content {
		if part.Type == "skill" && part.Path != "" {
			skills = append(skills, composerSkill{name: part.Name, path: part.Path})
		}
		if part.Type == "text" {
			frames, _ := decodeFileAttachments(part.Text)
			for _, frame := range frames {
				if name, path, _ := skillAttachmentFrame(frame); name != "" {
					skills = append(skills, composerSkill{name: name, path: path})
				}
			}
		}
	}
	return skills
}

type composerUserContent struct {
	Type     string                `json:"type"`
	Text     string                `json:"text"`
	Name     string                `json:"name"`
	Path     string                `json:"path"`
	Elements []composerTextElement `json:"textElements"`
}

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
			skills = append(skills, composerSkill{start: start, end: start + len(word), name: name, path: path})
		}
		at = start + len(word)
	}
	return skills
}

func unambiguousSkillBindings(text string, skills []composerSkill, spans []activityui.TextSpan) []composerSkill {
	paths := make(map[string]string)
	ambiguous := make(map[string]bool)
	for _, skill := range skills {
		if previous, ok := paths[skill.name]; ok && previous != skill.path {
			ambiguous[skill.name] = true
		} else if !ok {
			paths[skill.name] = skill.path
		}
	}
	var bindings []composerSkill
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
