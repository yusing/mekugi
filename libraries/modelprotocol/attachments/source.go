package attachments

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const sourcePrefix = "<mekugi-source-v1 bytes=\""
const sourceSuffix = "\n</mekugi-source-v1>"

// SourceRange identifies supplied bytes, excluding reader framing/diagnostics.
type SourceRange struct{ Start, End int }

// SourceRanges retains exact body boundaries in a message's final attachment.
// Lengths delimit arbitrary literal text, including marker lookalikes.
func SourceRanges(text string, bodies []SourceRange) string {
	if len(bodies) == 0 {
		return text
	}
	var ranges strings.Builder
	for i, body := range bodies {
		if i > 0 {
			ranges.WriteByte(',')
		}
		ranges.WriteString(strconv.Itoa(body.Start))
		ranges.WriteByte(':')
		ranges.WriteString(strconv.Itoa(body.End))
	}
	return sourcePrefix + strconv.Itoa(len(text)) + "\" bodies=\"" + ranges.String() + "\">\n" + text + sourceSuffix
}

// ProjectSources accepts only a complete terminal attachment. Earlier quoted
// envelopes and unselected diagnostics stay literal. Validation finishes before
// callbacks, so malformed input cannot seed request-local references.
func ProjectSources(text string, project func(string) string) string {
	if !strings.HasSuffix(text, sourceSuffix) {
		return text
	}
	for at := 0; at < len(text); {
		start := strings.Index(text[at:], sourcePrefix)
		if start < 0 {
			break
		}
		start += at
		at = start + len(sourcePrefix)
		if start > 0 && text[start-1] != '\n' {
			continue
		}
		headerEnd := strings.IndexByte(text[at:], '\n')
		if headerEnd < 0 {
			break
		}
		header, ok := strings.CutSuffix(text[at:at+headerEnd], "\">")
		length, coordinates, found := strings.Cut(header, "\" bodies=\"")
		n, err := strconv.Atoi(length)
		body := at + headerEnd + 1
		if !ok || !found || err != nil || n < 0 || strconv.Itoa(n) != length || n != len(text)-body-len(sourceSuffix) {
			continue
		}
		var ranges []SourceRange
		end := 0
		for coordinate := range strings.SplitSeq(coordinates, ",") {
			first, last, ok := strings.Cut(coordinate, ":")
			left, leftErr := strconv.Atoi(first)
			right, rightErr := strconv.Atoi(last)
			if !ok || leftErr != nil || rightErr != nil || strconv.Itoa(left) != first || strconv.Itoa(right) != last || left < end || right < left || right > n || left < n && !utf8.RuneStart(text[body+left]) || right < n && !utf8.RuneStart(text[body+right]) {
				return text
			}
			ranges = append(ranges, SourceRange{left, right})
			end = right
		}
		var result strings.Builder
		at = 0
		for i, span := range ranges {
			result.WriteString(text[body+at : body+span.Start])
			first := result.Len()
			result.WriteString(project(text[body+span.Start : body+span.End]))
			ranges[i] = SourceRange{first, result.Len()}
			at = span.End
		}
		result.WriteString(text[body+at : body+n])
		return text[:start] + SourceRanges(result.String(), ranges)
	}
	return text
}
