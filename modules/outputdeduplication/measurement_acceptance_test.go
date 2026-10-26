package outputdeduplication_test

import (
	"bytes"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func TestAcceptanceMeasuredProjectionSavings(t *testing.T) {
	p := registeredProjection(t)
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count := func(text string) int64 {
		t.Helper()
		n, err := codec.Count(text)
		if err != nil {
			t.Fatal(err)
		}
		return int64(n)
	}
	body := block("measured café <source>")
	for _, tc := range []struct {
		name   string
		input  []item
		output []string
		sign   int
	}{
		{"repeat", []item{tool("source", body), tool("repeat", body)}, []string{"O1:\n" + body, "See O1\n"}, 1},
		{"anchor only", []item{tool("source", body)}, []string{"O1:\n" + body}, -1},
		{"unchanged", []item{tool("small", "ok")}, []string{"ok"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := encode(t, tc.input)
			result := p.Prepare(input)(input)
			got := decode(t, result.Input)
			var want int64
			for i, output := range tc.output {
				if got[i]["output"] != output {
					t.Fatalf("output %d = %q, want %q", i, got[i]["output"], output)
				}
				// Only these decoded strings change; keys and other scalars cancel.
				want += count(tc.input[i]["output"].(string)) - count(output)
			}
			if result.Receipt == nil || !result.Receipt.TokensMeasured || result.Receipt.SavedTokens != want {
				t.Fatalf("receipt = %+v, want measured savings %d", result.Receipt, want)
			}
			if tc.sign > 0 && want <= 0 || tc.sign < 0 && want >= 0 || tc.sign == 0 && want != 0 {
				t.Fatalf("fixture savings = %d, want sign %d", want, tc.sign)
			}
			if tc.sign == 0 && !bytes.Equal(input, result.Input) {
				t.Fatal("unchanged input was rewritten")
			}
		})
	}
}

func TestAcceptanceSavingsIgnoreJSONFraming(t *testing.T) {
	p := registeredProjection(t)
	body := block("framing café <source>")
	input := encode(t, []item{tool("source", body), tool("repeat", body)})
	baseline := p.Prepare(input)(input)
	pretty := jsontext.Value(bytes.Clone(input))
	if err := pretty.Indent(jsontext.WithIndent("  ")); err != nil {
		t.Fatal(err)
	}
	// Decoded text stays identical despite whitespace and escape spelling.
	framed := []byte(strings.NewReplacer("café", `caf\u00e9`, "<", `\u003c`, ">", `\u003e`).Replace(string(pretty)))
	if bytes.Equal(input, framed) {
		t.Fatal("fixture did not change JSON framing")
	}
	result := p.Prepare(framed)(framed)
	if result.Receipt == nil || !result.Receipt.TokensMeasured || result.Receipt.SavedTokens != baseline.Receipt.SavedTokens {
		t.Fatalf("framed receipt = %+v, compact receipt = %+v", result.Receipt, baseline.Receipt)
	}
}
