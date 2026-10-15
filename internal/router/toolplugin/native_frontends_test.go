package toolplugin

import (
	"testing"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func nativeTokens(t *testing.T, source string) int {
	t.Helper()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(source)
	if err != nil {
		t.Fatal(err)
	}
	return count
}
