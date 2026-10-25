package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceSnapshotPackageDiscovery(t *testing.T) {
	workspaceFixture(t, []string{"./libraries/first"}, map[string]string{"libraries/first": prefix + "/libraries/first"})
	for path, source := range map[string]string{
		"render/snapshot_test.go": "package render_test\nimport \"testing\"\nfunc TestUISnapshotFrame(t *testing.T) {}\nfunc TestUISnapshotOther(t *testing.T) {}\n",
		"render/_ignored_test.go": "package render\nfunc TestUISnapshotIgnored(\n",
		"render/.ignored_test.go": "package render\nfunc TestUISnapshotIgnored(\n",
		"harness/check_test.go":   "package harness\nimport \"testing\"\nfunc TestSnapshotComparison(t *testing.T) {}\n",
		"tagged/snapshot_test.go": "//go:build ignore\n\npackage tagged\nfunc TestUISnapshotPlatform() {}\n",
		"plain/normal_test.go":    "package plain\n// TestUISnapshotComment\nfunc TestNormal() {}\nfunc (T) TestUISnapshotMethod() {}\n",
		"plain/source.go":         "package plain\nfunc TestUISnapshotNotATestFile() {}\n",
		"testdata/skip_test.go":   "package skipped\nfunc TestUISnapshotIgnored() {}\n",
		"vendor/skip_test.go":     "package skipped\nfunc TestUISnapshotIgnored() {}\n",
		".hidden/skip_test.go":    "package skipped\nfunc TestUISnapshotIgnored() {}\n",
		"_hidden/skip_test.go":    "package skipped\nfunc TestUISnapshotIgnored() {}\n",
		"nested/go.mod":           "module nested\n",
		"nested/skip_test.go":     "package nested\nfunc TestUISnapshotIgnored() {}\n",
	} {
		writeWorkspaceSource(t, filepath.Join("libraries/first", path), source)
	}
	members, err := workspaceMembers()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./libraries/first/harness/...", "./libraries/first/render/...", "./libraries/first/tagged/..."}
	got, err := expand([]string{"@snapshots"}, members)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("snapshot packages=%v error=%v, want %v", got, err, want)
	}
	// Only snapshot packages are compiled: plain contains invalid ordinary Go
	// code, and tagged has no files runnable on this platform.
	if err := run([]string{"run", "go", "test", "@snapshots", "-run", "TestSnapshot|TestUISnapshot", "-count=1"}); err != nil {
		t.Fatalf("run discovered snapshot packages: %v", err)
	}
	writeWorkspaceSource(t, "libraries/first/render/snapshot_test.go", "package render\nfunc TestUISnapshotBroken(\n")
	if _, err := expand([]string{"@snapshots"}, members); err == nil {
		t.Fatal("malformed snapshot declaration was ignored")
	}
}

func TestWorkspaceSnapshotPackageDiscoveryRejectsEmpty(t *testing.T) {
	workspaceFixture(t, []string{"./libraries/first"}, map[string]string{"libraries/first": prefix + "/libraries/first"})
	if err := run([]string{"run", "go", "test", "@snapshots"}); err == nil || !strings.Contains(err.Error(), "no snapshot test packages") {
		t.Fatalf("empty selection error=%v", err)
	}
}
