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

func TestMove(t *testing.T) {
	for _, tc := range []struct{ before, after, want string }{
		{"/w/internal/router/old.go", "/w/internal/router/new.go", "internal/router/{old.go=>new.go}"},
		{"internal/old/a.go", "internal/new/a.go", "internal/{old/a.go=>new/a.go}"},
		{"old.go", "new.go", "old.go => new.go"},
		{"same/old name", "same/new name", "same/{old name=>new name}"},
		{"/outside/old", "/outside/new", "/outside/{old=>new}"},
		{"/w/a.go", "/outside/b.go", "a.go => /outside/b.go"},
		{"a/old.go", "ab/new.go", "a/old.go => ab/new.go"},
		{"/w/x.txt.new", "/w/x.txt", "x.txt.new => x.txt"},
	} {
		if got := Move("/w", tc.before, tc.after); got != tc.want {
			t.Errorf("Move(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
		}
	}
}
