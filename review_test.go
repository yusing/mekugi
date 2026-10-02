package mekugi

import (
	"strings"
	"testing"
)

func TestReviewFiles(t *testing.T) {
	files := []ReviewFile{
		RenderReviewFile("old.txt", "", "removed\n", ""),
		RenderReviewFile("", "empty.txt", "", ""),
		RenderReviewFile("before.txt", "after.txt", "unchanged\n", "unchanged\n"),
		RenderReviewFile("end.txt", "end.txt", "a\r\nb", "a\nb\n"),
	}
	if len(files) != 4 {
		t.Fatalf("files = %#v", files)
	}
	for i, want := range []string{"-removed\n", `add "" -> "empty.txt"`, `move "before.txt" -> "after.txt"`, "-a\r\n-b\n\\ No newline at end of file\n+a\n+b\n"} {
		if !strings.Contains(files[i].Diff, want) {
			t.Errorf("file %d: %q missing %q", i, files[i].Diff, want)
		}
	}
	if strings.Contains(files[2].Diff, "@@") {
		t.Fatal("pure move should not invent content edits")
	}
}

func TestReviewActionClassifiesCapturedIdentity(t *testing.T) {
	for _, test := range []struct {
		file   ReviewFile
		action ReviewAction
		title  string
	}{
		{ReviewFile{AfterPath: "new"}, ReviewAdd, "Create"},
		{ReviewFile{BeforePath: "same", AfterPath: "same"}, ReviewUpdate, "Edit"},
		{ReviewFile{BeforePath: "old"}, ReviewDelete, "Delete"},
		{ReviewFile{BeforePath: "old", AfterPath: "new"}, ReviewMove, "Move"},
	} {
		if got := test.file.Action(); got != test.action || got.Title() != test.title {
			t.Errorf("Action(%+v) = %q/%q; want %q/%q", test.file, got, got.Title(), test.action, test.title)
		}
	}
}

func TestReviewPresentation(t *testing.T) {
	files := []ReviewFile{
		RenderReviewFile("file.txt", "file.txt", "--old\n", "++new\n"),
		RenderReviewFile("", "new.txt", "", "new"),
		RenderReviewFile("gone.txt", "", "gone\n", ""),
		RenderReviewFile("", "empty.txt", "", ""),
		RenderReviewFile("empty-old.txt", "", "", ""),
		RenderReviewFile("old.txt", "renamed.txt", "same\n", "same\n"),
	}
	for i, want := range [][2]int{{1, 1}, {1, 0}, {0, 1}, {0, 0}, {0, 0}, {0, 0}} {
		added, removed := files[i].LineCounts()
		if added != want[0] || removed != want[1] {
			t.Errorf("counts %d = +%d -%d, want +%d -%d", i, added, removed, want[0], want[1])
		}
		diff := files[i].UnifiedDiff()
		if i < 3 && !strings.HasPrefix(diff, "--- ") {
			t.Errorf("unified headers missing: %q", diff)
		}
		if i >= 3 && diff != files[i].Diff {
			t.Errorf("lost header-only change: %q", diff)
		}
		// Already compact records must render identically.
		file := files[i]
		file.Diff = diff
		if file.UnifiedDiff() != diff {
			t.Errorf("presentation is not stable: %#v", file)
		}
	}
}

func TestIncompleteReview(t *testing.T) {
	file := RenderIncompleteReviewFile("file", "file", "permission denied")
	if add, remove := file.LineCounts(); add != -1 || remove != -1 {
		t.Fatalf("invented counts: %d %d", add, remove)
	}
	if !strings.Contains(file.Diff, "incomplete history") || strings.Contains(file.Diff, "@@") {
		t.Fatalf("invented content: %s", file.Diff)
	}
	var composition ReviewComposition
	if err := composition.ApplyWithHighlight(file, false, false); err == nil || len(composition.FilesWithHighlights()) != 0 {
		t.Fatalf("incomplete capture composed: %v", err)
	}
}

func TestDirectoryReviewCannotInventContent(t *testing.T) {
	file := ReviewFile{BeforePath: "node_modules", AfterPath: "node_modules", Directory: true}
	if added, removed := file.LineCounts(); added != -1 || removed != -1 {
		t.Fatal("directory metadata supplied line counts")
	}
	for _, reverse := range []bool{false, true} {
		if _, err := file.Merge("", reverse, "test"); err == nil {
			t.Fatal("directory metadata became a replayable empty file")
		}
	}
	var composition ReviewComposition
	if err := composition.ApplyWithHighlight(file, false, false); err == nil {
		t.Fatal("directory metadata became composable content")
	}
}

func TestReviewWorkspaceDisplay(t *testing.T) {
	for _, tc := range []struct{ before, after, old, new string }{
		{"/w/old name", "/w/new name", "same\n", "same\n"},
		{"/w/old", "/w/new", "--- /w/source-row\n", "+++ /w/source-row\n"},
		{"", "/w/new", "", "new\n"},
		{"/w/old", "", "old\n", ""},
		{"/outside/old", "/outside/new", "old\n", "new\n"},
	} {
		file := RenderReviewFile(tc.before, tc.after, tc.old, tc.new)
		original := file
		want := RenderReviewFile(strings.TrimPrefix(tc.before, "/w/"), strings.TrimPrefix(tc.after, "/w/"), tc.old, tc.new).UnifiedDiff()
		if got := file.UnifiedDiffForWorkspace("/w"); got != want {
			t.Errorf("display = %q, want %q", got, want)
		}
		if file != original {
			t.Fatal("display changed retained evidence")
		}
	}
}

func TestBinaryReviewWorkspaceDisplay(t *testing.T) {
	file := RenderBinaryReviewFile("/w/old.bin", "/w/new.bin", 10, 12, "oldhash", "newhash")
	original := file
	want := RenderBinaryReviewFile("old.bin", "new.bin", 10, 12, "oldhash", "newhash").UnifiedDiff()
	if got := file.UnifiedDiffForWorkspace("/w"); got != want {
		t.Fatalf("binary display = %q, want %q", got, want)
	}
	if file != original {
		t.Fatal("display changed binary evidence")
	}
}
