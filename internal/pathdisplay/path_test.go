package pathdisplay

import (
	"path/filepath"
	"testing"
)

func TestForWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	tests := map[string]string{
		"":            "",
		"relative.go": "relative.go",
		filepath.Join(workspace, "nested", "inside.go"):      filepath.Join("nested", "inside.go"),
		filepath.Join(filepath.Dir(workspace), "outside.go"): filepath.Join(filepath.Dir(workspace), "outside.go"),
		workspace + "-other/file.go":                         workspace + "-other/file.go",
		workspace:                                            ".",
	}
	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			if got := ForWorkspace(workspace, path); got != want {
				t.Fatalf("ForWorkspace(%q, %q) = %q, want %q", workspace, path, got, want)
			}
		})
	}
}
