package retained

import (
	"path/filepath"
	"testing"
)

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
