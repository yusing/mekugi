package capturer

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"strings"

	"github.com/tiktoken-go/tokenizer"
)

// contentTokens counts decoded JSON keys and scalar values independently, not
// their wire spelling or JSON punctuation. Strings containing JSON/code remain
// literal text: escaping inside model-visible content is real token overhead.
// This is a local content estimate, never provider billing usage.
func contentTokens(payload []byte, codec tokenizer.Codec) (int, error) {
	if capturedPayloadLooksLikeSSE(payload) {
		total := 0
		for body := range sseData(payload) {
			if bytes.Equal(bytes.TrimSpace(body), []byte("[DONE]")) {
				continue
			}
			count, err := contentTokens(body, codec)
			if err != nil {
				return 0, err
			}
			total += count
		}
		return total, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return codec.Count(string(payload))
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return codec.Count(string(payload))
	}
	return decodedTokens(value, codec)
}

func decodedTokens(value any, codec tokenizer.Codec) (int, error) {
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
	case json.Number:
		return codec.Count(normalizedNumber(string(value)))
	case bool:
		if value {
			return codec.Count("true")
		}
		return codec.Count("false")
	default:
		return codec.Count("null")
	}
}

// Normalize decimal spelling without float rounding or exponent-sized allocation.
func normalizedNumber(value string) string {
	sign := ""
	if strings.HasPrefix(value, "-") {
		sign = "-"
		value = value[1:]
	}
	mantissa, exponent, _ := strings.Cut(strings.ToLower(value), "e")
	var power big.Int
	if exponent != "" {
		power.SetString(exponent, 10)
	}
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		power.Sub(&power, big.NewInt(int64(len(mantissa)-dot-1)))
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0"
	}
	digits := strings.TrimRight(mantissa, "0")
	power.Add(&power, big.NewInt(int64(len(mantissa)-len(digits))))
	if power.Sign() == 0 {
		return sign + digits
	}
	return sign + digits + "e" + power.String()
}

// Only assistant text participates in CTP output compression. Tool-call
// translation, reasoning, and generated commentary are not compression savings.
func measureOutputText(output []byte, codec tokenizer.Codec) (payloadMetrics, error) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(output, &rawItems); err != nil {
		return payloadMetrics{}, err
	}
	var result payloadMetrics
	for _, raw := range rawItems {
		var item struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Role != "assistant" {
			continue
		}
		var texts []string
		if item.Type == "" {
			var text string
			if json.Unmarshal(item.Content, &text) == nil {
				texts = append(texts, text)
			}
		}
		if len(texts) == 0 && (item.Type == "message" || item.Type == "") {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(item.Content, &parts) != nil {
				continue
			}
			for _, part := range parts {
				if part.Type == "output_text" || (item.Type == "" && part.Type == "text") {
					texts = append(texts, part.Text)
				}
			}
		}
		for _, text := range texts {
			n, err := codec.Count(text)
			if err != nil {
				return payloadMetrics{}, err
			}
			result.Bytes += uint64(len(text))
			result.Tokens += uint64(n)
		}
	}
	return result, nil
}
