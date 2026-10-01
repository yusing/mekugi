package toolplugin

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRegistryRequiresNeitherNodeNorRegexValidator(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	snapshot, err := Load(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.NodeExecutable != "" || len(snapshot.Plugins) != 1 || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("native snapshot: %+v", snapshot)
	}
	expected := map[string]bool{"mcat": true, "msymbol": true, "inspect_file": true, "mread": true, "mrun": true, "mchanges": true}
	for _, tool := range snapshot.Plugins[0].Tools {
		var spec struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Format      any    `json:"format"`
		}
		if err := json.Unmarshal(tool.Specification, &spec); err != nil {
			t.Fatal(err)
		}
		if !expected[spec.Name] || tool.NativeExecutor != spec.Name || spec.Description == "" || spec.Format != nil {
			t.Fatalf("native specification: %s / %q", tool.Specification, tool.NativeExecutor)
		}
		delete(expected, spec.Name)
	}
	if len(expected) > 0 {
		t.Fatalf("missing builtins: %v", expected)
	}
	if err := filepath.WalkDir(snapshot.Root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			t.Errorf("native snapshot unexpectedly contains runtime asset: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
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
