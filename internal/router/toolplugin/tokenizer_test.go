package toolplugin

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func TestGPT5TokenizerMatchesFixtures(t *testing.T) {
	t.Parallel()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/gpt5_tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Text   string `json:"text"`
		Tokens int    `json:"tokens"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		got, err := codec.Count(fixture.Text)
		if err != nil {
			t.Fatalf("count %q: %v", fixture.Text, err)
		}
		if got != fixture.Tokens {
			t.Errorf("count %q = %d, want %d", fixture.Text, got, fixture.Tokens)
		}
	}
}

func TestFormatOutputNeedsNoNodeRuntimeOrToolDeclarations(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result, err := FormatOutput(t.Context(), []string{"3", "head", "hello world", "diagnostic"})
	if err != nil || result.Stdout != "hello world" || result.Stderr != "diagn" || result.ExitCode != 0 {
		t.Fatalf("isolated formatter = %+v, %v", result, err)
	}
}
