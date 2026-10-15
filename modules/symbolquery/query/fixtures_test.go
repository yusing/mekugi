package query

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func nativeFixture(t *testing.T, name, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func executeQuery(t *testing.T, args ...string) command.Output {
	t.Helper()
	output, err := Execute(t.Context(), args, nil)
	if err != nil {
		t.Fatalf("msymbol %q: %v", args, err)
	}
	return output
}

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
