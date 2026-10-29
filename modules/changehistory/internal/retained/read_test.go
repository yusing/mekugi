package retained

import (
	"path/filepath"
	"testing"
)

func TestChangeReadOperands(t *testing.T) {
	t.Parallel()
	options, err := parseChangeRead([]string{"amber1..2", "bar/baz1", "baz2", "--summary"}, "/caller")
	if err != nil || len(options.ids) != 2 || len(options.paths) != 2 || options.cwd != "/caller" {
		t.Fatalf("operands: %+v, %v", options, err)
	}
	if _, err := parseChangeRead([]string{"amber1", "--workspace", "/owner"}, "/caller"); err == nil {
		t.Fatal("removed workspace option accepted")
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
}

func TestAbsoluteChangeFiltersSkipEvidenceScan(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file")
	options := changeReadOptions{ids: []string{"amber1"}, paths: []string{path}}
	index := changeIndex{Changes: map[string]trackedChange{"amber1": {Correlation: "missing", Calls: []trackedCall{{ID: "missing"}}}}}
	if err := (&store{directory: t.TempDir()}).resolveChangePaths(&options, index); err != nil || len(options.resolvedPaths) != 1 || options.resolvedPaths[0] != path {
		t.Fatalf("absolute filter scanned evidence: %+v, %v", options, err)
	}
}
