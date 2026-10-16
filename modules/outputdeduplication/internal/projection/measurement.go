// Source: capturer/content_tokens.go:17:84@91f6a320, request-only estimation.
package projection

import (
	"bytes"
	jsonv1 "encoding/json"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

// EstimateSavings measures request-local content, never provider billing usage.
// Failure cannot prevent projection; the caller publishes an unmeasured receipt.
func EstimateSavings(before, after []byte) (saved int64, measured bool) {
	if bytes.Equal(before, after) {
		return 0, true
	}
	codec, err := tokenizer.New()
	if err != nil {
		return 0, false
	}
	original, err := contentTokens(before, codec)
	if err != nil {
		return 0, false
	}
	projected, err := contentTokens(after, codec)
	if err != nil {
		return 0, false
	}
	return int64(original - projected), true
}

// Decoded keys/scalars exclude wire framing; embedded JSON/code stays literal.
func contentTokens(payload []byte, codec *tokenizer.Codec) (int, error) {
	value, err := responses.DecodeJSONValue(payload)
	if err != nil {
		return codec.Count(string(payload))
	}
	return decodedTokens(value, codec)
}

func decodedTokens(value any, codec *tokenizer.Codec) (int, error) {
	switch value := value.(type) {
	case map[string]any:
		total := 0
		for key, child := range value {
			n, err := codec.Count(key)
			if err != nil {
				return 0, err
			}
			total += n
			n, err = decodedTokens(child, codec)
			if err != nil {
				return 0, err
			}
			total += n
		}
		return total, nil
	case []any:
		total := 0
		for _, child := range value {
			n, err := decodedTokens(child, codec)
			if err != nil {
				return 0, err
			}
			total += n
		}
		return total, nil
	case string:
		return codec.Count(value)
	case jsonv1.Number:
		return codec.Count(responses.CanonicalNumber(string(value)))
	case bool:
		if value {
			return codec.Count("true")
		}
		return codec.Count("false")
	default:
		return codec.Count("null")
	}
}
