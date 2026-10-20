package query

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeSymbolRejectsRust(t *testing.T) {
	path := nativeFixture(t, "valid.rs", "fn visible() {}\n")
	out := executeQuery(t, "--workspace", filepath.Dir(path), "def", path, "visible")
	if out.ExitCode != 1 || out.Stdout != "" || !strings.Contains(out.Stderr, "unsupported msymbol source format") {
		t.Fatalf("Rust gained semantic support: %+v", out)
	}
}
