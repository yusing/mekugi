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
