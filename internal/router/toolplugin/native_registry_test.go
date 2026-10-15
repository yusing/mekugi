package toolplugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRegistryRequiresNeitherNodeNorRegexValidator(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	expected := map[string]bool{"mcat": true, "msymbol": true, "inspect_file": true, "mread": true, "mchanges": true}
	for _, tool := range Builtins() {
		if err := tool.Validate(); err != nil || !expected[tool.Name] {
			t.Fatalf("native specification: %+v: %v", tool, err)
		}
		delete(expected, tool.Name)
	}
	if len(expected) > 0 {
		t.Fatalf("missing builtins: %v", expected)
	}
	source := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(source, []byte("package p\nfunc Native() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mcat", "inspect_file"} {
		result, err := ExecuteBuiltin(t.Context(), name, []string{source})
		if err != nil || result.ExitCode != 0 || result.Stdout == "" {
			t.Fatalf("%s without runtime: %+v, %v", name, result, err)
		}
	}
}
