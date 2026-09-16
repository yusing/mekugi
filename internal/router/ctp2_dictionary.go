package router

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ctp2TokenizedString struct {
	value   string
	tokens  []uint
	offsets []int
}

type ctp2SeedKey struct {
	hash   uint64
	length int
}

type ctp2SeedOccurrence struct {
	start int
}

type ctp2SubstringOccurrence struct {
	start int
	end   int
}

type ctp2SubstringCandidate struct {
	value       string
	tokens      int
	boundary    int
	occurrences map[ctp2SubstringOccurrence]struct{}
	saving      int
}

func (c *ctp2Codec) stringDefinitions(value string) ([]ctp2Definition, error) {
	tokenized, err := c.tokenizeString(value)
	if err != nil {
		return nil, err
	}
	refTokens, err := c.tokens.Count("@{0}")
	if err != nil {
		return nil, fmt.Errorf("estimate CTP/2 reference: %w", err)
	}
	seedLength := refTokens + 1
	seeds := make(map[ctp2SeedKey]ctp2SeedOccurrence)
	candidatesByValue := make(map[string]*ctp2SubstringCandidate)
	for start := 0; start+seedLength <= len(tokenized.tokens); {
		key := ctp2TokenSeedKey(tokenized.tokens[start : start+seedLength])
		first, found := seeds[key]
		if !found {
			seeds[key] = ctp2SeedOccurrence{start: start}
			start++
			continue
		}
		if !slices.Equal(tokenized.tokens[first.start:first.start+seedLength], tokenized.tokens[start:start+seedLength]) {
			start++
			continue
		}
		firstStart, currentStart := first.start, start
		firstEnd, currentEnd := firstStart+seedLength, currentStart+seedLength
		for firstStart > 0 && currentStart > 0 &&
			tokenized.tokens[firstStart-1] == tokenized.tokens[currentStart-1] && firstEnd <= currentStart-1 {
			firstStart--
			currentStart--
		}
		for firstEnd < len(tokenized.tokens) && currentEnd < len(tokenized.tokens) &&
			tokenized.tokens[firstEnd] == tokenized.tokens[currentEnd] && firstEnd < currentStart {
			firstEnd++
			currentEnd++
		}
		firstStart, firstEnd, currentStart, currentEnd = trimCTP2MatchToReadableBoundaries(
			tokenized, firstStart, firstEnd, currentStart, currentEnd,
		)
		if firstEnd-firstStart < seedLength {
			start++
			continue
		}
		candidateValue := value[tokenized.offsets[firstStart]:tokenized.offsets[firstEnd]]
		candidate := candidatesByValue[candidateValue]
		if candidate == nil {
			candidate = &ctp2SubstringCandidate{
				value:       candidateValue,
				tokens:      firstEnd - firstStart,
				boundary:    ctp2BoundaryScore(value, tokenized.offsets[firstStart], tokenized.offsets[firstEnd]),
				occurrences: make(map[ctp2SubstringOccurrence]struct{}),
			}
			candidatesByValue[candidateValue] = candidate
		}
		candidate.occurrences[ctp2SubstringOccurrence{start: tokenized.offsets[firstStart], end: tokenized.offsets[firstEnd]}] = struct{}{}
		candidate.occurrences[ctp2SubstringOccurrence{start: tokenized.offsets[currentStart], end: tokenized.offsets[currentEnd]}] = struct{}{}
		start = max(start+1, currentEnd)
	}

	tagTokens, err := c.tokens.Count(ctp2ReferenceTag)
	if err != nil {
		return nil, fmt.Errorf("estimate CTP/2 reference tag: %w", err)
	}
	candidates := make([]ctp2SubstringCandidate, 0, len(candidatesByValue))
	for _, candidate := range candidatesByValue {
		occurrences := countCTP2NonoverlappingOccurrences(candidate.occurrences)
		if occurrences < 2 {
			continue
		}
		quoted, _ := marshalProtocolJSON(candidate.value)
		definitionTokens, err := c.tokens.Count("0=" + string(quoted) + "\n")
		if err != nil {
			return nil, fmt.Errorf("estimate CTP/2 definition: %w", err)
		}
		indirectionCost := 1 + occurrences
		candidate.saving = candidate.tokens*occurrences - definitionTokens - refTokens*occurrences - tagTokens - indirectionCost
		if candidate.saving > 0 {
			candidates = append(candidates, *candidate)
		}
	}
	slices.SortFunc(candidates, func(left, right ctp2SubstringCandidate) int {
		if left.saving != right.saving {
			return right.saving - left.saving
		}
		if left.boundary != right.boundary {
			return right.boundary - left.boundary
		}
		if len(left.value) != len(right.value) {
			return len(right.value) - len(left.value)
		}
		return strings.Compare(left.value, right.value)
	})
	definitions := make([]ctp2Definition, len(candidates))
	for index, candidate := range candidates {
		definitions[index] = ctp2Definition{id: ctp2Base36(index), value: candidate.value}
	}
	return definitions, nil
}

func (c *ctp2Codec) tokenizeString(value string) (ctp2TokenizedString, error) {
	tokens, pieces, err := c.tokens.Encode(value)
	if err != nil {
		return ctp2TokenizedString{}, fmt.Errorf("tokenize CTP/2 string: %w", err)
	}
	if len(tokens) != len(pieces) || strings.Join(pieces, "") != value {
		return ctp2TokenizedString{}, errors.New("tokenize CTP/2 string: non-lossless token pieces")
	}
	offsets := make([]int, len(pieces)+1)
	for index, piece := range pieces {
		offsets[index+1] = offsets[index] + len(piece)
	}
	return ctp2TokenizedString{value: value, tokens: tokens, offsets: offsets}, nil
}

func ctp2TokenSeedKey(tokens []uint) ctp2SeedKey {
	const multiplier = uint64(1099511628211)
	var hash uint64 = 1469598103934665603
	for _, token := range tokens {
		hash = (hash ^ (uint64(token) + 1)) * multiplier
	}
	return ctp2SeedKey{hash: hash, length: len(tokens)}
}

func trimCTP2MatchToReadableBoundaries(value ctp2TokenizedString, firstStart, firstEnd, currentStart, currentEnd int) (int, int, int, int) {
	for firstEnd-firstStart > 0 &&
		(!ctp2TextBoundary(value.value, value.offsets[firstStart]) || !ctp2TextBoundary(value.value, value.offsets[currentStart])) {
		firstStart++
		currentStart++
	}
	for firstEnd-firstStart > 0 &&
		(!ctp2TextBoundary(value.value, value.offsets[firstEnd]) || !ctp2TextBoundary(value.value, value.offsets[currentEnd])) {
		firstEnd--
		currentEnd--
	}
	return firstStart, firstEnd, currentStart, currentEnd
}

func ctp2TextBoundary(value string, offset int) bool {
	if offset == 0 || offset == len(value) {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:offset])
	next, _ := utf8.DecodeRuneInString(value[offset:])
	return unicode.IsSpace(previous) || unicode.IsPunct(previous) || unicode.IsSpace(next) || unicode.IsPunct(next)
}

func ctp2BoundaryScore(value string, start, end int) int {
	score := 0
	if ctp2TextBoundary(value, start) {
		score++
	}
	if ctp2TextBoundary(value, end) {
		score++
	}
	return score
}

func countCTP2NonoverlappingOccurrences(occurrences map[ctp2SubstringOccurrence]struct{}) int {
	ordered := slices.Collect(maps.Keys(occurrences))
	slices.SortFunc(ordered, func(left, right ctp2SubstringOccurrence) int {
		if left.start != right.start {
			return left.start - right.start
		}
		return left.end - right.end
	})
	count, end := 0, -1
	for _, occurrence := range ordered {
		if occurrence.start < end {
			continue
		}
		count++
		end = occurrence.end
	}
	return count
}

func ctp2Base36(value int) string {
	return strconv.FormatInt(int64(value), 36)
}

type ctp2StringEncoder struct {
	byPrefix map[byte][]ctp2Definition
}

func newCTP2StringEncoder(definitions []ctp2Definition) ctp2StringEncoder {
	byPrefix := make(map[byte][]ctp2Definition)
	for _, definition := range definitions {
		if definition.value == "" {
			continue
		}
		prefix := definition.value[0]
		byPrefix[prefix] = append(byPrefix[prefix], definition)
	}
	for prefix := range byPrefix {
		slices.SortFunc(byPrefix[prefix], func(left, right ctp2Definition) int {
			if len(left.value) != len(right.value) {
				return len(right.value) - len(left.value)
			}
			return strings.Compare(left.id, right.id)
		})
	}
	return ctp2StringEncoder{byPrefix: byPrefix}
}

func (e ctp2StringEncoder) encode(value string) (string, map[string]int) {
	literal := encodeCTP2LiteralString(value)
	if len(e.byPrefix) == 0 {
		return literal, nil
	}
	var body strings.Builder
	references := make(map[string]int)
	bodyEndsWithLiteralAt := false
	for index := 0; index < len(value); {
		matched := false
		// A reference immediately after a literal @ would look like the @@{ID}
		// literal escape. Leave that occurrence native so data cannot become framing.
		if !bodyEndsWithLiteralAt {
			for _, definition := range e.byPrefix[value[index]] {
				if !strings.HasPrefix(value[index:], definition.value) {
					continue
				}
				fmt.Fprintf(&body, "@{%s}", definition.id)
				references[definition.id]++
				index += len(definition.value)
				matched = true
				break
			}
		}
		if matched {
			bodyEndsWithLiteralAt = false
			continue
		}
		if _, length, ok := parseCTP2Reference(value[index:]); ok {
			body.WriteByte('@')
			body.WriteString(value[index : index+length])
			index += length
			bodyEndsWithLiteralAt = false
			continue
		}
		body.WriteByte(value[index])
		bodyEndsWithLiteralAt = value[index] == '@'
		index++
	}
	if len(references) == 0 {
		return literal, nil
	}
	return ctp2ReferenceTag + body.String(), references
}

func encodeCTP2LiteralString(value string) string {
	if strings.HasPrefix(value, ctp2ReferenceTag) || strings.HasPrefix(value, ctp2LiteralTag) ||
		strings.HasPrefix(value, ctp2DictionaryTag) || strings.HasPrefix(value, ctp2VisibleLinesTag) {
		return ctp2LiteralTag + value
	}
	return value
}

func (c *ctp2Codec) encodeContentLocalString(value string) (string, int, error) {
	literal := encodeCTP2LiteralString(value)
	literalTokens, err := c.count([]byte(literal))
	if err != nil {
		return "", 0, err
	}
	definitions, err := c.stringDefinitions(value)
	if err != nil {
		return "", 0, err
	}
	encoded, references := newCTP2StringEncoder(definitions).encode(value)
	retained := make([]ctp2Definition, 0, len(definitions))
	for _, definition := range definitions {
		if references[definition.id] >= 2 {
			retained = append(retained, definition)
		}
	}
	if len(retained) != len(definitions) {
		for index := range retained {
			retained[index].id = ctp2Base36(index)
		}
		definitions = retained
		// Removing a competing greedy match can only expose more matches for a
		// retained definition, so one final encoding reaches the required fixed point.
		encoded, _ = newCTP2StringEncoder(definitions).encode(value)
	}
	if len(definitions) == 0 || !strings.HasPrefix(encoded, ctp2ReferenceTag) {
		return literal, literalTokens, nil
	}
	dictionary := renderCTP2Dictionary(definitions)
	compact := dictionary + encoded
	compactTokens, err := c.count([]byte(compact))
	if err != nil {
		return "", 0, err
	}
	if compactTokens >= literalTokens {
		return literal, literalTokens, nil
	}
	decoded, err := decodeCTP2String(compact, nil, upstreamJSONBufferBytes)
	if err != nil {
		return "", 0, err
	}
	if decoded != value {
		return "", 0, errors.New("CTP/2 content-local encoding changed text")
	}
	return compact, compactTokens, nil
}

func renderCTP2Dictionary(definitions []ctp2Definition) string {
	var dictionary strings.Builder
	dictionary.WriteString(ctp2DictionaryTag)
	for _, definition := range definitions {
		encoded, _ := marshalProtocolJSON(definition.value)
		fmt.Fprintf(&dictionary, "%s=%s\n", definition.id, encoded)
	}
	dictionary.WriteString(ctp2DictionaryEnd)
	return dictionary.String()
}
