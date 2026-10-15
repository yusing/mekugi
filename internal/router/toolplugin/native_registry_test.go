package toolplugin

import (
	"testing"
)

func TestNativeRegistryRequiresNeitherNodeNorRegexValidator(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	expected := map[string]bool{"mread": true, "mchanges": true}
	for _, tool := range Builtins() {
		if err := tool.Validate(); err != nil || !expected[tool.Name] {
			t.Fatalf("native specification: %+v: %v", tool, err)
		}
		delete(expected, tool.Name)
	}
	if len(expected) > 0 {
		t.Fatalf("missing builtins: %v", expected)
	}
}
