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
	for i, want := range []string{
		" file.txt | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n",
		" new.txt | 1 +\n 1 file changed, 1 insertion(+)\n",
		" gone.txt | 1 -\n 1 file changed, 1 deletion(-)\n",
		" empty.txt | 0\n 1 file changed\n",
		" empty-old.txt | 0\n 1 file changed\n",
		" old.txt => renamed.txt | 0\n 1 file changed\n",
	} {
		if got := ReviewStat(files[i : i+1]); got != want {
			t.Errorf("summary %d = %q; want %q", i, got, want)
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
		if file.UnifiedDiff() != diff || ReviewStat([]ReviewFile{file}) != want {
			t.Errorf("presentation is not stable: %#v", file)
		}
	}
}

func TestReviewStatAlignmentAndScaling(t *testing.T) {
	files := []ReviewFile{
		RenderReviewFile("", "large.txt", "", strings.Repeat("new\n", 100)),
		RenderReviewFile("small", "small", "old\n", "new\n"),
		RenderReviewFile("", "empty", "", ""),
	}
	want := " large.txt | 100 " + strings.Repeat("+", 40) + "\n" +
		" small     |   2 +-\n empty     |   0\n" +
		" 3 files changed, 101 insertions(+), 1 deletion(-)\n"
	if got := ReviewStat(files); got != want {
		t.Fatalf("stat = %q; want %q", got, want)
	}
	if got := ReviewStat(nil); got != "" {
		t.Fatalf("empty stat = %q", got)
	}
	file := ReviewFile{AfterPath: "bad\n\t\x1b.txt"}
	if got := ReviewStat([]ReviewFile{file}); got != " \"bad\\n\\t\\x1b.txt\" | 0\n 1 file changed\n" {
		t.Fatalf("unsafe path stat = %q", got)
	}
}

func TestReviewStatUnicodeAlignment(t *testing.T) {
	files := []ReviewFile{
		{AfterPath: "界.txt"},
		{AfterPath: "a.txt"},
		{AfterPath: "e\u0301.txt"},
	}
	want := " 界.txt | 0\n a.txt  | 0\n e\u0301.txt  | 0\n 3 files changed\n"
	if got := ReviewStat(files); got != want {
		t.Fatalf("stat = %q; want %q", got, want)
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
	if stat := ReviewStat([]ReviewFile{file}); !strings.Contains(stat, "unavailable") || strings.Contains(stat, "+0") {
		t.Fatalf("stat: %s", stat)
	}
	var composition ReviewComposition
	if err := composition.ApplyWithHighlight(file, false, false); err == nil || len(composition.FilesWithHighlights()) != 0 {
		t.Fatalf("incomplete capture composed: %v", err)
	}
}
