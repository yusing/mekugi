package projection

import (
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func TestContentTokensIgnoreJSONFraming(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	variants := []string{
		`{"input":"<hello>&\nworld","tools":[{"name":"lookup"}],"n":1000}`,
		" { \"n\": 1.0e3, \"tools\": [ { \"name\" : \"lookup\" } ], \"input\" : \"\\u003chello\\u003e\\u0026\\nworld\" } ",
	}
	want, err := contentTokens([]byte(variants[0]), codec)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range variants[1:] {
		got, err := contentTokens([]byte(value), codec)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("framing changed content tokens: %v vs %v", got, want)
		}
	}
	literal, _ := contentTokens([]byte(`{"input":"<"}`), codec)
	escapedText, _ := contentTokens([]byte(`{"input":"\\u003c"}`), codec)
	if escapedText <= literal {
		t.Fatalf("literal model-visible escape overhead lost: %d <= %d", escapedText, literal)
	}
	for _, value := range []string{"1", "1.0", "0.1e1", "100e-2"} {
		if got := responses.CanonicalNumber(value); got != "1" {
			t.Fatalf("normalize %s = %s", value, got)
		}
	}
}
