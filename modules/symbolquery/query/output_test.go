package query

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
)

func TestQuotedPathEncoding(t *testing.T) {
	for _, path := range []string{"", "ordinary.go", "quote\"\\/\x00\n\t<>&", "日本語/🧪", "invalid\xff\xed\xa0\x80"} {
		// Retain the previous path-encoding contract while narrowing the serializer.
		want, err := json.Marshal(path, jsontext.AllowInvalidUTF8(true))
		if err != nil {
			t.Fatal(err)
		}
		if got := quotePath(path); got != string(want) {
			t.Fatalf("quote %q = %q, want %q", path, got, want)
		}
	}
}
