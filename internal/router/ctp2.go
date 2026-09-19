package router

import (
	"encoding/json"

	"github.com/yusing/mekugi/internal/tokenizer"
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
