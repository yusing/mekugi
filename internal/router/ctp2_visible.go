package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

func newCTP2VisibleLineEncoder(codec *ctp2Codec) *ctp2VisibleLineEncoder {
	return &ctp2VisibleLineEncoder{
		codec: codec,
		pairs: make(map[ctp2VisibleLineSeed][]ctp2VisibleLineLocation),
		long:  make(map[string][]ctp2VisibleLineLocation),
	}
}

func (e *ctp2VisibleLineEncoder) encodeString(locator, value string) (string, error) {
	lines := slices.Collect(strings.Lines(value))
	if validCTP2VisibleLocator(locator) {
		// The current output becomes a response-visible source after encoding, so
		// earlier-source suffixes must already be unique against its locator.
		e.locators = append(e.locators, locator)
		defer e.addSource(locator, lines)
	}
	local, localTokens, err := e.codec.encodeContentLocalString(value)
	if err != nil {
		return "", err
	}
	visible, references, err := e.encode(lines)
	if err != nil || references == 0 {
		return local, err
	}
	visibleTokens, err := e.codec.count([]byte(visible))
	if err != nil {
		return "", err
	}
	if visibleTokens >= localTokens {
		return local, nil
	}
	decoded, err := decodeCTP2VisibleLines(visible, e.sources, upstreamJSONBufferBytes)
	if err != nil {
		return "", err
	}
	if decoded != value {
		return "", errors.New("CTP/2 visible-line encoding changed text")
	}
	return visible, nil
}

func validCTP2VisibleLocator(locator string) bool {
	return locator != "" && !strings.ContainsAny(locator, ",\r\n")
}

func (e *ctp2VisibleLineEncoder) encode(lines []string) (string, int, error) {
	var compact strings.Builder
	compact.WriteString(ctp2VisibleLinesTag)
	literalStart := 0
	references := 0
	for index := 0; index < len(lines); {
		location, count, suffix := e.longestMatch(lines, index)
		if count == 0 {
			index++
			continue
		}
		reference := fmt.Sprintf("=%s,%d,%d\n", suffix, location.line+1, count)
		profitable, err := e.visibleReferenceProfitable(reference, lines[index:index+count])
		if err != nil {
			return "", 0, err
		}
		if !profitable {
			index++
			continue
		}
		writeCTP2VisibleLiteral(&compact, lines[literalStart:index])
		compact.WriteString(reference)
		index += count
		literalStart = index
		references++
	}
	writeCTP2VisibleLiteral(&compact, lines[literalStart:])
	return compact.String(), references, nil
}

func (e *ctp2VisibleLineEncoder) visibleReferenceProfitable(reference string, lines []string) (bool, error) {
	referenceText := ctp2VisibleLinesTag + reference
	var literal strings.Builder
	literal.WriteString(ctp2VisibleLinesTag)
	writeCTP2VisibleLiteral(&literal, lines)
	referenceTokens, err := e.codec.count([]byte(referenceText))
	if err != nil {
		return false, err
	}
	literalTokens, err := e.codec.count([]byte(literal.String()))
	if err != nil {
		return false, err
	}
	return referenceTokens < literalTokens, nil
}

func (e *ctp2VisibleLineEncoder) sourceSuffix(source int) string {
	locator := e.sources[source].locator
	for length := 1; length <= len(locator); length++ {
		suffix := locator[len(locator)-length:]
		if !utf8.ValidString(suffix) {
			continue
		}
		matches := 0
		for _, candidate := range e.locators {
			if strings.HasSuffix(candidate, suffix) {
				matches++
			}
		}
		if matches == 1 {
			return suffix
		}
	}
	return ""
}

func (e *ctp2VisibleLineEncoder) longestMatch(lines []string, index int) (ctp2VisibleLineLocation, int, string) {
	var candidates []ctp2VisibleLineLocation
	if index+1 < len(lines) {
		candidates = append(candidates, e.pairs[ctp2VisibleLineSeed{first: lines[index], second: lines[index+1]}]...)
	}
	if len(lines[index]) >= 128 {
		candidates = append(candidates, e.long[lines[index]]...)
	}
	var best ctp2VisibleLineLocation
	bestCount := 0
	bestSuffix := ""
	for _, candidate := range candidates {
		suffix := e.sourceSuffix(candidate.source)
		if suffix == "" {
			continue
		}
		source := e.sources[candidate.source].lines
		count := 0
		for index+count < len(lines) && candidate.line+count < len(source) && lines[index+count] == source[candidate.line+count] {
			count++
		}
		if count > bestCount {
			best, bestCount, bestSuffix = candidate, count, suffix
		}
	}
	return best, bestCount, bestSuffix
}

func (e *ctp2VisibleLineEncoder) addSource(locator string, lines []string) {
	source := len(e.sources)
	e.sources = append(e.sources, ctp2VisibleLineSource{locator: locator, lines: slices.Clone(lines)})
	for index := range lines {
		location := ctp2VisibleLineLocation{source: source, line: index}
		if index+1 < len(lines) {
			seed := ctp2VisibleLineSeed{first: lines[index], second: lines[index+1]}
			e.pairs[seed] = append(e.pairs[seed], location)
		}
		if len(lines[index]) >= 128 {
			e.long[lines[index]] = append(e.long[lines[index]], location)
		}
	}
}

func writeCTP2VisibleLiteral(compact *strings.Builder, lines []string) {
	if len(lines) == 0 {
		return
	}
	encoded, _ := marshalProtocolJSON(strings.Join(lines, ""))
	compact.WriteByte('+')
	compact.Write(encoded)
	compact.WriteByte('\n')
}

func decodeCTP2VisibleLines(value string, sources []ctp2VisibleLineSource, limit int) (string, error) {
	operations, ok := strings.CutPrefix(value, ctp2VisibleLinesTag)
	if !ok {
		return value, nil
	}
	if operations == "" {
		return "", errors.New("decode CTP/2 visible lines: empty operation")
	}
	var decoded strings.Builder
	for operations != "" {
		line, rest, ok := strings.Cut(operations, "\n")
		if !ok {
			return "", errors.New("decode CTP/2 visible lines: unterminated operation")
		}
		operations = rest
		if line == "" {
			return "", errors.New("decode CTP/2 visible lines: empty operation")
		}
		switch line[0] {
		case '=':
			parts := strings.Split(line[1:], ",")
			if len(parts) != 3 || parts[0] == "" {
				return "", errors.New("decode CTP/2 visible lines: invalid reference")
			}
			var sourceLines []string
			matches := 0
			for _, source := range sources {
				if strings.HasSuffix(source.locator, parts[0]) {
					sourceLines = source.lines
					matches++
				}
			}
			start, startErr := strconv.Atoi(parts[1])
			count, countErr := strconv.Atoi(parts[2])
			if matches != 1 || startErr != nil || countErr != nil || start <= 0 || count <= 0 {
				return "", errors.New("decode CTP/2 visible lines: reference is out of range")
			}
			start--
			if start > len(sourceLines) || count > len(sourceLines)-start {
				return "", errors.New("decode CTP/2 visible lines: reference is out of range")
			}
			for _, sourceLine := range sourceLines[start : start+count] {
				if err := appendCTP2Decoded(&decoded, sourceLine, limit); err != nil {
					return "", err
				}
			}
		case '+':
			var literal string
			if json.Unmarshal([]byte(line[1:]), &literal) != nil {
				return "", errors.New("decode CTP/2 visible lines: invalid literal")
			}
			if err := appendCTP2Decoded(&decoded, literal, limit); err != nil {
				return "", err
			}
		default:
			return "", errors.New("decode CTP/2 visible lines: unknown operation")
		}
	}
	return decoded.String(), nil
}

func cloneCTP2VisibleLineSources(sources []ctp2VisibleLineSource) []ctp2VisibleLineSource {
	cloned := make([]ctp2VisibleLineSource, len(sources))
	for index, source := range sources {
		cloned[index] = ctp2VisibleLineSource{locator: source.locator, lines: slices.Clone(source.lines)}
	}
	return cloned
}

func decodeCTP2String(value string, sources []ctp2VisibleLineSource, limit int) (string, error) {
	if literal, ok := strings.CutPrefix(value, ctp2LiteralTag); ok {
		if len(literal) > limit {
			return "", errors.New("decoded CTP/2 string exceeds the router buffer budget")
		}
		return literal, nil
	}
	if strings.HasPrefix(value, ctp2VisibleLinesTag) {
		return decodeCTP2VisibleLines(value, sources, limit)
	}
	if strings.HasPrefix(value, ctp2ReferenceTag) {
		return "", errors.New("decode CTP/2 string: local reference has no dictionary")
	}
	if !strings.HasPrefix(value, ctp2DictionaryTag) {
		if len(value) > limit {
			return "", errors.New("decoded CTP/2 string exceeds the router buffer budget")
		}
		return value, nil
	}
	body, definitions, err := decodeCTP2Dictionary(value[len(ctp2DictionaryTag):])
	if err != nil {
		return "", err
	}
	return decodeCTP2ReferenceBody(body, definitions, limit)
}

func decodeCTP2Dictionary(value string) (string, map[string]string, error) {
	definitions := make(map[string]string)
	for {
		line, rest, ok := strings.Cut(value, "\n")
		if !ok {
			return "", nil, errors.New("decode CTP/2 dictionary: missing END")
		}
		value = rest
		if line == "END" {
			if len(definitions) == 0 {
				return "", nil, errors.New("decode CTP/2 dictionary: empty dictionary")
			}
			if !strings.HasPrefix(value, ctp2ReferenceTag) {
				return "", nil, errors.New("decode CTP/2 dictionary: missing reference body")
			}
			return value, definitions, nil
		}
		id, encoded, ok := strings.Cut(line, "=")
		if !ok || !validCTP2ReferenceID(id) {
			return "", nil, errors.New("decode CTP/2 dictionary: malformed definition")
		}
		if _, exists := definitions[id]; exists {
			return "", nil, fmt.Errorf("decode CTP/2 dictionary: duplicate definition %q", id)
		}
		var definition string
		if err := json.Unmarshal([]byte(encoded), &definition); err != nil {
			return "", nil, fmt.Errorf("decode CTP/2 dictionary definition %q: %w", id, err)
		}
		definitions[id] = definition
	}
}

func decodeCTP2ReferenceBody(value string, definitions map[string]string, limit int) (string, error) {
	encoded, ok := strings.CutPrefix(value, ctp2ReferenceTag)
	if !ok {
		return "", errors.New("decode CTP/2 string: missing reference body")
	}
	var decoded strings.Builder
	for encoded != "" {
		at := strings.IndexByte(encoded, '@')
		if at < 0 {
			if err := appendCTP2Decoded(&decoded, encoded, limit); err != nil {
				return "", err
			}
			break
		}
		if err := appendCTP2Decoded(&decoded, encoded[:at], limit); err != nil {
			return "", err
		}
		encoded = encoded[at:]
		if strings.HasPrefix(encoded, "@@{") {
			if _, length, ok := parseCTP2Reference(encoded[1:]); ok {
				if err := appendCTP2Decoded(&decoded, encoded[1:1+length], limit); err != nil {
					return "", err
				}
				encoded = encoded[1+length:]
				continue
			}
		}
		id, length, ok := parseCTP2Reference(encoded)
		if !ok {
			if err := appendCTP2Decoded(&decoded, "@", limit); err != nil {
				return "", err
			}
			encoded = encoded[1:]
			continue
		}
		definition, exists := definitions[id]
		if !exists {
			return "", fmt.Errorf("decode CTP/2 string: unknown reference %q", id)
		}
		if err := appendCTP2Decoded(&decoded, definition, limit); err != nil {
			return "", err
		}
		encoded = encoded[length:]
	}
	return decoded.String(), nil
}

func parseCTP2Reference(value string) (string, int, bool) {
	if !strings.HasPrefix(value, "@{") {
		return "", 0, false
	}
	end := strings.IndexByte(value[2:], '}')
	if end < 0 {
		return "", 0, false
	}
	id := value[2 : 2+end]
	if !validCTP2ReferenceID(id) {
		return "", 0, false
	}
	return id, 3 + end, true
}

func validCTP2ReferenceID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'z' {
				return false
			}
		}
	}
	return true
}

func appendCTP2Decoded(output *strings.Builder, value string, limit int) error {
	if limit < output.Len() || len(value) > limit-output.Len() {
		return errors.New("decoded CTP/2 string exceeds the router buffer budget")
	}
	output.WriteString(value)
	return nil
}
