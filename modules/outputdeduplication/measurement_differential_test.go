package outputdeduplication_test

import (
	jsonv1 "encoding/json"
	"fmt"
	"strings"
	"testing"

	expect "github.com/yusing/goutils/testing"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

// Count the entire decoded request, independently of which leaves the projection
// considers eligible. Keys and scalars are separate tokenization boundaries.
func fullContentTokens(t *testing.T, codec *tokenizer.Codec, raw []byte) int64 {
	t.Helper()
	value, err := responses.DecodeJSONValue(raw)
	expect.NoError(t, err)
	count := func(text string) int64 {
		n, err := codec.Count(text)
		expect.NoError(t, err)
		return int64(n)
	}
	var visit func(any) int64
	visit = func(value any) int64 {
		switch value := value.(type) {
		case map[string]any:
			var total int64
			for key, child := range value {
				total += count(key) + visit(child)
			}
			return total
		case []any:
			var total int64
			for _, child := range value {
				total += visit(child)
			}
			return total
		case string:
			return count(value)
		case jsonv1.Number:
			return count(responses.CanonicalNumber(string(value)))
		case bool:
			return count(fmt.Sprint(value))
		case nil:
			return count("null")
		default:
			t.Fatalf("unexpected decoded scalar %T", value)
			return 0
		}
	}
	return visit(value)
}

func TestAcceptanceProjectionSavingsFullRequestDifferential(t *testing.T) {
	p := registeredProjection(t)
	codec, err := tokenizer.New()
	expect.NoError(t, err)
	body := block("café <source> \\\" JSON")
	header := "Chunk ID: differential\nWall time: 0.2 seconds\nProcess exited with code 0\nFinal output:\n"
	file := fmt.Sprintf("Attached file %q (UTF-8 bytes 0:%d of %d; %s):\n", "/never-open/café.txt", len(body), len(body), attachments.ReadGuidance)
	skill := fmt.Sprintf("Attached skill %q from %q (UTF-8 bytes 0:%d of %d; %s):\n", "differential", "/never-open/SKILL.md", len(body), len(body), attachments.ReadGuidance)
	envelope := (attachments.Snapshot{ID: "differential", Frames: []string{file + body, skill + body}}).Encode()
	unchanged := []item{
		{"role": "developer", "content": []item{textPart(strings.Repeat("unchanged developer instruction <tag> café\n", 1024))}},
		{"role": "user", "content": []item{textPart(strings.Repeat("ordinary prompt, not an attachment\n", 512))},
			"metadata": item{"large": strings.Repeat("unchanged metadata\n", 512), "scalars": []any{nil, true, false, jsonv1.Number("1.000e3"), jsonv1.Number("9007199254740993")}}},
	}
	for _, tc := range []struct {
		name  string
		items []item
	}{
		{"mixed multipart", []item{tool("seed", body), {"type": "custom_tool_call_output", "call_id": "multipart", "output": []item{
			textPart(header + body), {"type": "input_image", "image_url": "data:image/png;base64,unchanged"}, textPart("ok"), textPart(body),
		}}}},
		{"complete attachment envelope", []item{{"role": "user", "content": []item{textPart("ordinary leading prompt"), textPart(envelope), textPart("ordinary trailing prompt")}}, tool("attachment-repeat", body)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := encode(t, append(append([]item{}, unchanged...), tc.items...))
			result := p.Prepare(input)(input)
			expect.NotNil(t, result.Receipt)
			expect.True(t, result.Receipt.TokensMeasured)
			expect.True(t, result.Receipt.OutputRepetitions > 0)
			want := fullContentTokens(t, codec, input) - fullContentTokens(t, codec, result.Input)
			expect.True(t, want > 0)
			expect.Equal(t, result.Receipt.SavedTokens, want)
			expect.Equal(t, decode(t, result.Input)[:len(unchanged)], decode(t, input)[:len(unchanged)])
		})
	}
}
