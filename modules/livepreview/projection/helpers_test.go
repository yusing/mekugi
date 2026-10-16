package projection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func mustShellStatement(t *testing.T, command string) *syntax.Stmt {
	t.Helper()
	statements, _, _, parsed := shellsyntax.StreamingStatements(command, "/")
	if !parsed || len(statements) != 1 {
		t.Fatalf("%q did not parse", command)
	}
	return statements[0]
}
