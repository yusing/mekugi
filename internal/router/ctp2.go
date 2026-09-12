package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

const (
	ctp2ReferenceTag    = "!ctp2 R\n"
	ctp2LiteralTag      = "!ctp2 L\n"
	ctp2DictionaryTag   = "!ctp2 D\n"
	ctp2DictionaryEnd   = "END\n"
	ctp2VisibleLinesTag = "!V"
)

type ctp2Codec struct {
	tokens tokenizer.Codec
}

type ctp2InstructionCarrier uint8

const (
	ctp2CarrierNone ctp2InstructionCarrier = iota
	ctp2CarrierTopLevel
	ctp2CarrierDeveloperMessage
)

type ctp2RequestView struct {
	carrier  ctp2InstructionCarrier
	input    responsesInput
	hasInput bool
	hasTools bool
	catalog  *responsesToolCatalog
}

// responsesInput keeps provider-owned JSON raw while exposing its two standard
// shapes to the router.
type responsesInput struct {
	raw   json.RawMessage
	text  *string
	items []json.RawMessage
	array bool
}
type responsesTextPart struct {
	raw      json.RawMessage
	typeName string
	text     *string
}

type ctp2ResponseTransform struct {
	sources []ctp2VisibleLineSource
}

type ctp2Definition struct {
	id    string
	value string
}

type ctp2VisibleLineSeed struct {
	first  string
	second string
}

type ctp2VisibleLineLocation struct {
	source int
	line   int
}

type ctp2VisibleLineSource struct {
	locator string
	lines   []string
}

type ctp2VisibleLineEncoder struct {
	codec    *ctp2Codec
	locators []string
	sources  []ctp2VisibleLineSource
	pairs    map[ctp2VisibleLineSeed][]ctp2VisibleLineLocation
	long     map[string][]ctp2VisibleLineLocation
}

func newCTP2Codec() (*ctp2Codec, error) {
	tokens, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		return nil, fmt.Errorf("load GPT-5 tokenizer for CTP/2: %w", err)
	}
	return &ctp2Codec{tokens: tokens}, nil
}

func (c *ctp2Codec) prepareRequest(request *parsedResponsesRequest, nativeBody []byte) (*ctp2ResponseTransform, []byte, error) {
	if c == nil {
		return nil, nil, nil
	}
	transformed := maps.Clone(request.fields)
	view, err := decodeCTP2RequestView(transformed, request.responseTools())
	if err != nil {
		return nil, nativeBody, nil
	}
	if view.carrier == ctp2CarrierNone {
		return nil, nativeBody, nil
	}

	visible := newCTP2VisibleLineEncoder(c)
	encodeLocal := func(value string) string {
		encoded, _, encodeErr := c.encodeContentLocalString(value)
		if encodeErr != nil {
			err = errors.Join(err, encodeErr)
			return value
		}
		return encoded
	}
	if view.hasInput {
		if view.input.text != nil {
			*view.input.text = encodeLocal(*view.input.text)
		} else {
			if view.input.array {
				for _, group := range view.catalog.additional {
					projected, projectErr := projectCTP2AdditionalTools(group, encodeLocal)
					if projectErr != nil {
						err = errors.Join(err, projectErr)
						continue
					}
					view.input.items[group.itemIndex] = projected
				}
			}
			var preserveDeveloper func(string) string
			if view.carrier == ctp2CarrierDeveloperMessage {
				preserveDeveloper = func(value string) string { return value }
			}
			found, transformErr := transformCTP2Input(&view.input, encodeLocal, preserveDeveloper, visible, request.model() == grokModel)
			err = errors.Join(err, transformErr)
			if preserveDeveloper != nil && !found {
				return nil, nativeBody, nil
			}
		}
		if err == nil {
			transformed["input"], err = view.input.encode()
		}
	}
	if view.hasTools {
		projected, projectErr := projectCTP2ToolSection(view.catalog.top, encodeLocal)
		if projectErr != nil {
			err = errors.Join(err, projectErr)
		} else {
			transformed["tools"] = projected
		}
	}
	if err != nil {
		return nil, nativeBody, nil
	}

	compactBody, err := request.wireBody(transformed)
	if err != nil {
		return nil, nativeBody, nil
	}
	request.fields = transformed
	return &ctp2ResponseTransform{
		sources: cloneCTP2VisibleLineSources(visible.sources),
	}, compactBody, nil
}

func (c *ctp2Codec) count(value []byte) (int, error) {
	count, err := c.tokens.Count(string(value))
	if err != nil {
		return 0, fmt.Errorf("estimate CTP/2 tokens: %w", err)
	}
	if count < 0 {
		return 0, errors.New("estimate CTP/2 tokens: negative count")
	}
	return count, nil
}

// decodeCTP2RequestView decodes and analyzes a CTP/2 request's input and tool catalog.
func decodeCTP2RequestView(fields map[string]json.RawMessage, catalog *responsesToolCatalog) (ctp2RequestView, error) {
	view := ctp2RequestView{catalog: catalog}
	var instructions string
	if json.Unmarshal(fields["instructions"], &instructions) == nil && strings.TrimSpace(instructions) != "" {
		view.carrier = ctp2CarrierTopLevel
	}
	if raw, ok := fields["input"]; ok {
		if catalog.inputItems != nil {
			view.input = responsesInput{
				raw:   bytes.Clone(raw),
				items: slices.Clone(catalog.inputItems),
				array: true,
			}
		} else {
			input, err := decodeResponsesInput(raw)
			if err != nil {
				return ctp2RequestView{}, fmt.Errorf("decode CTP/2 input: %w", err)
			}
			view.input = input
		}
		view.hasInput = true
		if view.carrier == ctp2CarrierNone {
			found, err := transformFirstDeveloperText(&view.input, func(value string) string { return value })
			if err != nil {
				return ctp2RequestView{}, err
			}
			if found {
				view.carrier = ctp2CarrierDeveloperMessage
			}
		}
	}
	if view.carrier == ctp2CarrierNone {
		return view, nil
	}
	if _, ok := fields["tools"]; ok {
		view.hasTools = true
	}
	return view, nil
}

// decodeResponsesInput decodes a Responses input field into its structured form.
func decodeResponsesInput(raw json.RawMessage) (responsesInput, error) {
	input := responsesInput{raw: bytes.Clone(raw)}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		input.text = new(text)
		return input, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		input.items = items
		input.array = true
		return input, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return responsesInput{}, err
	}
	return input, nil
}

// encode marshals the responsesInput back to JSON.
func (input responsesInput) encode() (json.RawMessage, error) {
	if input.text != nil {
		return marshalProtocolJSON(*input.text)
	}
	if input.array {
		return marshalProtocolJSON(input.items)
	}
	return bytes.Clone(input.raw), nil
}

// transformCTP2Input applies transformations to a Responses input array.
func transformCTP2Input(
	input *responsesInput,
	transformString, transformFirstDeveloper func(string) string,
	visible *ctp2VisibleLineEncoder,
	preserveNativeArguments bool,
) (bool, error) {
	if input == nil || !input.array {
		return false, nil
	}
	developerTransformed := false
	for index, raw := range input.items {
		item, ok := decodeResponsesItem(raw)
		if !ok {
			continue
		}
		switch item.Type {
		case "message":
			if !developerTransformed && item.Role == "developer" {
				content, found, err := transformCTP2DeveloperContent(item.Content, transformString, transformFirstDeveloper)
				if err != nil {
					return false, err
				}
				if found {
					developerTransformed = true
					item.setContent(content)
					input.items[index] = mustMarshalJSON(item)
					continue
				}
			}
			content, changed, err := transformCTP2Content(item.Content, transformString, isCTP2TextPart)
			if err != nil {
				return false, err
			}
			if changed {
				item.setContent(content)
			}
		case "custom_tool_call":
			if item.Input != nil {
				item.setInput(transformString(*item.Input))
			}
		case "custom_tool_call_output", "function_call_output":
			output, changed, err := transformCTP2VisibleLineOutput(item.Output, item.CallID, visible)
			if err != nil {
				return false, err
			}
			if changed {
				item.setOutput(output)
			}
		case "function_call":
			// Chat function arguments must remain JSON, not a CTP text envelope.
			if item.Arguments != nil && !preserveNativeArguments {
				item.setArguments(transformString(*item.Arguments))
			}
		}
		input.items[index] = mustMarshalJSON(item)
	}
	return developerTransformed, nil
}

// transformCTP2DeveloperContent keeps the selected final text part native as the
// instruction carrier while allowing independent sibling parts to use CTP/2.
func transformCTP2DeveloperContent(
	raw json.RawMessage,
	transformOther, transformCarrier func(string) string,
) (json.RawMessage, bool, error) {
	if transformCarrier == nil {
		return raw, false, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return mustMarshalJSON(transformCarrier(text)), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	carrier := -1
	for index, part := range slices.Backward(parts) {
		if part.text != nil && isCTP2InputTextPart(part.typeName) {
			carrier = index
			break
		}
	}
	if carrier < 0 {
		return raw, false, nil
	}
	for index := range parts {
		part := &parts[index]
		if part.text == nil || !isCTP2InputTextPart(part.typeName) {
			continue
		}
		transformed := transformOther(*part.text)
		if index == carrier {
			transformed = transformCarrier(*part.text)
		}
		updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(transformed))
		if err != nil {
			return nil, false, err
		}
		part.raw = updated
	}
	encoded, err := encodeResponsesTextParts(parts)
	return encoded, true, err
}

// transformFirstDeveloperText finds and transforms the first developer message's text content.
func transformFirstDeveloperText(input *responsesInput, transform func(string) string) (bool, error) {
	if input == nil || !input.array {
		return false, nil
	}
	for index, raw := range input.items {
		item, ok := decodeResponsesItem(raw)
		if !ok || item.Type != "message" || item.Role != "developer" {
			continue
		}
		content, found, err := transformLastTextContent(item.Content, transform)
		if err != nil {
			return false, err
		}
		if found {
			item.setContent(content)
			input.items[index] = mustMarshalJSON(item)
			return true, nil
		}
	}
	return false, nil
}

// transformLastTextContent finds and transforms the last text content part.
func transformLastTextContent(raw json.RawMessage, transform func(string) string) (json.RawMessage, bool, error) {
	if transform == nil {
		return raw, false, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return mustMarshalJSON(transform(text)), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	for index := len(parts) - 1; index >= 0; index-- {
		part := &parts[index]
		if part.text != nil && isCTP2InputTextPart(part.typeName) {
			updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(transform(*part.text)))
			if err != nil {
				return nil, false, err
			}
			part.raw = updated
			encoded, err := encodeResponsesTextParts(parts)
			return encoded, true, err
		}
	}
	return raw, false, nil
}

// transformCTP2Content transforms text parts in message content.
func transformCTP2Content(
	raw json.RawMessage,
	transform func(string) string,
	isTextPart func(string) bool,
) (json.RawMessage, bool, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return mustMarshalJSON(transform(text)), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	changed := false
	for index := range parts {
		part := &parts[index]
		if part.text == nil || !isTextPart(part.typeName) {
			continue
		}
		updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(transform(*part.text)))
		if err != nil {
			return nil, false, err
		}
		part.raw = updated
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := encodeResponsesTextParts(parts)
	return encoded, true, err
}

// decodeResponsesTextParts decodes message content into text parts.
func decodeResponsesTextParts(raw json.RawMessage) ([]responsesTextPart, bool) {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	parts := make([]responsesTextPart, len(values))
	for index, value := range values {
		var fields struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		_ = json.Unmarshal(value, &fields)
		parts[index] = responsesTextPart{raw: value, typeName: fields.Type, text: fields.Text}
	}
	return parts, true
}

// encodeResponsesTextParts encodes text parts back to JSON.
func encodeResponsesTextParts(parts []responsesTextPart) (json.RawMessage, error) {
	values := make([]json.RawMessage, len(parts))
	for index := range parts {
		values[index] = parts[index].raw
	}
	return marshalProtocolJSON(values)
}

// isCTP2TextPart reports whether a type name is any CTP/2 text part.
func isCTP2TextPart(typeName string) bool {
	return typeName == "input_text" || typeName == "output_text" || typeName == "text"
}

// isCTP2InputTextPart reports whether a type name is an input text part.
func isCTP2InputTextPart(typeName string) bool {
	return typeName == "input_text" || typeName == "text"
}

// isCTP2AssistantTextPart reports whether a type name is an assistant text part.
func isCTP2AssistantTextPart(typeName string) bool {
	return typeName == "output_text" || typeName == "text"
}

// projectCTP2AdditionalTools transforms an additional_tools item for CTP/2.
func projectCTP2AdditionalTools(group *responsesAdditionalTools, transform func(string) string) (json.RawMessage, error) {
	item := maps.Clone(group.item)
	if !group.tools.present {
		return marshalProtocolJSON(item)
	}
	tools, err := projectCTP2ToolSection(group.tools, transform)
	if err != nil {
		return nil, err
	}
	item["tools"] = tools
	return marshalProtocolJSON(item)
}

// projectCTP2ToolSection transforms a tool section for CTP/2.
func projectCTP2ToolSection(section *responsesToolSection, transform func(string) string) (json.RawMessage, error) {
	if section == nil || !section.array {
		return sectionRawTools(section), nil
	}
	definitions := slices.Clone(section.rawTools)
	for index, node := range section.nodes {
		if node == nil {
			continue
		}
		tool := maps.Clone(node.definition.fields)
		var description *string
		if json.Unmarshal(node.definition.rawField("description"), &description) == nil && description != nil {
			tool["description"] = mustMarshalJSON(transform(*description))
		}
		if node.nested != nil {
			nested, err := projectCTP2ToolSection(node.nested, transform)
			if err != nil {
				return nil, err
			}
			tool["tools"] = nested
		}
		encoded, err := marshalProtocolJSON(tool)
		if err != nil {
			return nil, err
		}
		definitions[index] = encoded
	}
	return marshalProtocolJSON(definitions)
}

// sectionRawTools returns the raw tools JSON from a section.
func sectionRawTools(section *responsesToolSection) json.RawMessage {
	if section == nil {
		return nil
	}
	return section.raw
}

func transformCTP2VisibleLineOutput(
	raw json.RawMessage,
	callID string,
	encoder *ctp2VisibleLineEncoder,
) (json.RawMessage, bool, error) {
	encode := func(locator, value string) (string, error) {
		encoded, err := encoder.encodeString(locator, value)
		if err != nil {
			return "", err
		}
		return encoded, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		encoded, err := encode(callID, text)
		if err != nil {
			return nil, false, err
		}
		return mustMarshalJSON(encoded), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	changed := false
	for index := range parts {
		part := &parts[index]
		if part.text == nil || !isCTP2TextPart(part.typeName) {
			continue
		}
		locator := callID
		if locator != "" {
			locator += "/" + ctp2Base36(index)
		}
		encoded, err := encode(locator, *part.text)
		if err != nil {
			return nil, false, err
		}
		updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(encoded))
		if err != nil {
			return nil, false, err
		}
		part.raw = updated
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := encodeResponsesTextParts(parts)
	return encoded, true, err
}

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

func (t *ctp2ResponseTransform) TransformJSON(payload []byte) ([]byte, error) {
	return t.transformJSON(payload)
}

func (t *ctp2ResponseTransform) transformJSON(payload []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil || response == nil {
		return nil, errors.New("decode CTP/2 response")
	}
	if raw, ok := response["output"]; ok {
		var output []responsesItem
		if err := json.Unmarshal(raw, &output); err != nil {
			return nil, errors.New("decode CTP/2 response output")
		}
		for index := range output {
			if err := t.transformOutputItem(&output[index]); err != nil {
				return nil, err
			}
		}
		response["output"] = mustMarshalJSON(output)
	}
	encoded, err := marshalProtocolJSON(response)
	if err != nil {
		return nil, err
	}
	if len(encoded) > upstreamJSONBufferBytes {
		return nil, errors.New("decoded CTP/2 response exceeds the router buffer budget")
	}
	return encoded, nil
}

func (t *ctp2ResponseTransform) TransformSSE(payload []byte) (transformed [][]byte, err error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil || event == nil {
		return [][]byte{payload}, nil
	}
	typeName := jsonString(event, "type")
	switch typeName {
	case "response.output_item.added", "response.output_item.done":
		if item, ok := decodeResponsesItem(event["item"]); ok {
			if err := t.transformOutputItem(&item); err != nil {
				return nil, err
			}
			event["item"] = mustMarshalJSON(item)
		}
	case "response.output_text.done":
		compact := jsonString(event, "text")
		decoded, err := decodeCTP2String(compact, t.sources, upstreamJSONBufferBytes)
		if err != nil {
			return nil, err
		}
		event["text"] = mustMarshalJSON(decoded)
	case "response.content_part.added", "response.content_part.done":
		var part map[string]json.RawMessage
		if json.Unmarshal(event["part"], &part) == nil && part != nil {
			if err := t.transformTextPart(part); err != nil {
				return nil, err
			}
			event["part"] = mustMarshalJSON(part)
		}
	}
	if rawResponse, ok := event["response"]; ok {
		trimmed := bytes.TrimSpace(rawResponse)
		if len(trimmed) != 0 && trimmed[0] == '{' {
			decoded, err := t.transformJSON(rawResponse)
			if err != nil {
				return nil, err
			}
			event["response"] = decoded
		}
	}
	encoded, err := marshalProtocolJSON(event)
	if err != nil {
		return nil, err
	}
	if len(encoded) > upstreamJSONBufferBytes {
		return nil, errors.New("decoded CTP/2 stream event exceeds the router buffer budget")
	}
	return [][]byte{encoded}, nil
}

func (t *ctp2ResponseTransform) Finish(bool) error {
	return nil
}

func (t *ctp2ResponseTransform) transformOutputItem(item *responsesItem) error {
	if item.Type != "message" || item.Role != "assistant" {
		return nil
	}
	if len(item.Content) == 0 {
		return nil
	}
	decoded, err := t.transformMessageContent(item.Content)
	if err != nil {
		return err
	}
	item.setContent(decoded)
	return nil
}

func (t *ctp2ResponseTransform) transformMessageContent(raw json.RawMessage) (json.RawMessage, error) {
	var transformErr error
	transformed, _, err := transformCTP2Content(raw, func(text string) string {
		decoded, decodeErr := decodeCTP2String(text, t.sources, upstreamJSONBufferBytes)
		if decodeErr != nil {
			transformErr = errors.Join(transformErr, decodeErr)
			return text
		}
		return decoded
	}, isCTP2AssistantTextPart)
	if err != nil {
		return nil, err
	}
	if transformErr != nil {
		return nil, transformErr
	}
	return transformed, nil
}

func (t *ctp2ResponseTransform) transformTextPart(part map[string]json.RawMessage) error {
	typeName := jsonString(part, "type")
	if typeName != "output_text" && typeName != "text" {
		return nil
	}
	compact := jsonString(part, "text")
	decoded, err := decodeCTP2String(compact, t.sources, upstreamJSONBufferBytes)
	if err != nil {
		return err
	}
	part["text"] = mustMarshalJSON(decoded)
	return nil
}
