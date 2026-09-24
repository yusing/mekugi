package toolplugin

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestGPT5TokenizerMatchesPluginFixtures(t *testing.T) {
	t.Parallel()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("tests/testdata/gpt5_tokens.json")
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

func newFormatterFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	// Copy only the host and generated JavaScript. The formatter must not
	// instantiate the source-analysis core or load a tool declaration.
	builtins, err := fs.Sub(runtimeFiles, "dist")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, snapshotDirectory, "builtin"), builtins); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, snapshotDirectory, "builtin", "tools.js")); err != nil {
		t.Fatal(err)
	}
	host, err := runtimeFiles.ReadFile(hostFilename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, hostFilename), host, 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return node, root
}

func TestFormatOutputDoesNotLoadToolDeclarationsOrWASM(t *testing.T) {
	t.Parallel()
	node, root := newFormatterFixture(t)
	result, err := FormatOutput(t.Context(), node, root, []string{"3", "head", "hello world", "diagnostic"})
	if err != nil || result.Stdout != "hello world" || result.Stderr != "diagn" || result.ExitCode != 0 {
		t.Fatalf("isolated formatter = %+v, %v", result, err)
	}
}
