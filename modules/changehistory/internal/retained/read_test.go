package retained

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChangeWorkspaceMissingSuffix(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, suffix := range []string{"", "removed", filepath.Join("removed", "workspace")} {
		options, err := parseChangeRead([]string{"amber1", "--workspace", filepath.Join(alias, suffix)}, root)
		if err != nil || options.workspace != filepath.Join(root, suffix) {
			t.Fatalf("suffix %q: workspace=%q error=%v", suffix, options.workspace, err)
		}
	}
	for _, operation := range []string{"revert", "apply"} {
		if _, err := parseChangeRead([]string{operation, "amber1", "--workspace", filepath.Join(alias, "removed")}, root); err == nil {
			t.Fatalf("%s accepted a missing workspace", operation)
		}
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := parseChangeRead([]string{"amber1", "--workspace", alias}, filepath.Dir(alias)); err == nil {
		t.Fatal("dangling symlink accepted as a lexical workspace")
	}
}

func TestChangePathSpellings(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	for _, test := range []struct {
		path, recorded string
		want           bool
	}{
		{"file.txt", filepath.Join(workspace, "file.txt"), true},
		{filepath.Join(workspace, "file.txt"), "file.txt", true},
		{"./file.txt", "file.txt", true},
		{"other.txt", "file.txt", false},
		{"file.txt", "", false},
	} {
		got := changePathMatches(changeReadOptions{workspace: workspace, paths: []string{test.path}}, test.recorded)
		if got != test.want {
			t.Errorf("%+v: got %v", test, got)
		}
	}
	if changePathMatches(changeReadOptions{paths: []string{"./file.txt", "/file.txt"}}, "file.txt") {
		t.Fatal("no-directory selection must not invent a path base")
	}
	options, err := parseChangeRead([]string{"amber1", "--workspace", ""}, workspace)
	if err != nil || options.workspace != "" {
		t.Fatalf("no-directory selection: %+v, %v", options, err)
	}
	if changePathMatches(options, "file.txt") {
		t.Fatal("empty selection unexpectedly matched")
	}
}
