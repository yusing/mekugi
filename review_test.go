package mekugi

import (
	"strconv"
	"strings"
	"testing"
)

func TestReviewFiles(t *testing.T) {
	files := reviewFiles([]change{
		{kind: changeDelete, originalPath: "old.txt", original: "removed\n"},
		{kind: changeAdd, path: "empty.txt"},
		{kind: changeUpdate, originalPath: "before.txt", path: "after.txt", original: "unchanged\n", content: "unchanged\n"},
		{kind: changeUpdate, originalPath: "end.txt", path: "end.txt", original: "a\r\nb", content: "a\nb\n"},
	})
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

func TestHostReviewCapturesFormattedState(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "sample.go", "", 0o644)
	edits := []FileEdit{{Path: "sample.go", Script: "append " + strconv.Quote("package sample\nvar X=1\n")}}
	result, err := TranslateForHostAt(t.Context(), root, edits, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ReviewFiles) != 1 || !strings.Contains(result.ReviewFiles[0].Diff, "+var X = 1\n") {
		t.Fatalf("review = %#v", result.ReviewFiles)
	}
	rejected, err := TranslateForHostAt(t.Context(), root, []FileEdit{{Path: "sample.go", Script: "append " + strconv.Quote("invalid go\n")}}, "")
	if err == nil || len(rejected.ReviewFiles) != 0 {
		t.Fatalf("rejected review = %#v, err = %v", rejected.ReviewFiles, err)
	}
}

func TestReviewPresentation(t *testing.T) {
	files := reviewFiles([]change{
		{kind: changeUpdate, originalPath: "file.txt", path: "file.txt", original: "--old\n", content: "++new\n"},
		{kind: changeAdd, path: "new.txt", content: "new"},
		{kind: changeDelete, originalPath: "gone.txt", original: "gone\n"},
		{kind: changeAdd, path: "empty.txt"},
		{kind: changeDelete, originalPath: "empty-old.txt"},
		{kind: changeUpdate, originalPath: "old.txt", path: "renamed.txt", original: "same\n", content: "same\n"},
	})
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
	files := reviewFiles([]change{
		{kind: changeAdd, path: "large.txt", content: strings.Repeat("new\n", 100)},
		{kind: changeUpdate, originalPath: "small", path: "small", original: "old\n", content: "new\n"},
		{kind: changeAdd, path: "empty"},
	})
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
