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

// sourceEnvelope accepts only a complete terminal attachment. Earlier quoted
// envelopes and unselected diagnostics stay outside its source ranges.
func sourceEnvelope(text string) (start, body, n int, ranges []SourceRange, valid bool) {
	if !strings.HasSuffix(text, sourceSuffix) {
		return
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
		end := 0
		for coordinate := range strings.SplitSeq(coordinates, ",") {
			if coordinates == "" {
				break
			}
			first, last, ok := strings.Cut(coordinate, ":")
			left, leftErr := strconv.Atoi(first)
			right, rightErr := strconv.Atoi(last)
			if !ok || leftErr != nil || rightErr != nil || strconv.Itoa(left) != first || strconv.Itoa(right) != last || left < end || right < left || right > n || left < n && !utf8.RuneStart(text[body+left]) || right < n && !utf8.RuneStart(text[body+right]) {
				return 0, 0, 0, nil, false
			}
			ranges = append(ranges, SourceRange{left, right})
			end = right
		}
		return start, body, n, ranges, true
	}
	return 0, 0, 0, nil, false
}

// SourceContent is one native reader result. Body bytes are not parsed as receipts.
type SourceContent struct{ Reference, Body, Error string }

// SourceContents decodes native output framing without interpreting supplied text.
func SourceContents(text string) []SourceContent {
	_, body, n, ranges, valid := sourceEnvelope(text)
	if !valid {
		return nil
	}
	var results []SourceContent
	at, index := 0, 0
	for at < n {
		rest := text[body+at : body+n]
		if rest[0] == '\n' {
			at++
			continue
		}
		line, _, _ := strings.Cut(rest, "\n")
		header, found := strings.CutPrefix(line, "Source ")
		quoted, err := strconv.QuotedPrefix(header)
		if !found || err != nil {
			return nil
		}
		reference, err := strconv.Unquote(quoted)
		if err != nil {
			return nil
		}
		ending := header[len(quoted):]
		at += len(line) + 1
		value := SourceContent{Reference: reference}
		if ending == ":" {
			if index >= len(ranges) || ranges[index].Start != at {
				return nil
			}
			span := ranges[index]
			value.Body = text[body+span.Start : body+span.End]
			at = span.End
			index++
		} else if reason, ok := strings.CutPrefix(ending, ": CONTENT NOT ATTACHED: "); ok {
			value.Error, err = strconv.Unquote(reason)
			if err != nil {
				return nil
			}
		} else {
			return nil
		}
		results = append(results, value)
	}
	if index != len(ranges) {
		return nil
	}
	return results
}
