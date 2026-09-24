package mekugi

import (
	"fmt"
	"strings"
	"testing"
)

func numberedLines(count int, change map[int]string) string {
	var text strings.Builder
	for i := 1; i <= count; i++ {
		if line, ok := change[i]; ok {
			text.WriteString(line)
			continue
		}
		fmt.Fprintf(&text, "line %s%c\n", strings.Repeat("x", i%3), rune('a'+i%26))
	}
	return text.String()
}

func TestReviewMergeRevertsAndReappliesCleanly(t *testing.T) {
	before := numberedLines(30, nil)
	after := numberedLines(30, map[int]string{5: "changed five\n", 20: "changed twenty\nadded\n"})
	file := RenderReviewFile("a.txt", "a.txt", before, after)
	reverted, err := file.Merge(after, true, "revert")
	if err != nil || reverted.Content != before || reverted.Conflicts != 0 || len(reverted.Rejected) != 0 {
		t.Fatalf("revert = %+v, %v", reverted, err)
	}
	applied, err := file.Merge(before, false, "apply")
	if err != nil || applied.Content != after || applied.Conflicts != 0 {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	again, err := file.Merge(before, true, "revert")
	if err != nil || again.Content != before || again.Satisfied != 2 {
		t.Fatalf("repeated revert = %+v, %v", again, err)
	}
}

func TestReviewMergeFollowsShiftedContent(t *testing.T) {
	before := numberedLines(30, nil)
	after := numberedLines(30, map[int]string{20: "changed twenty\n"})
	file := RenderReviewFile("a.txt", "a.txt", before, after)
	// Unrelated lines inserted above move the hunk.
	shifted := "new top\nnew top 2\n" + after
	reverted, err := file.Merge(shifted, true, "revert")
	if err != nil || reverted.Content != "new top\nnew top 2\n"+before || reverted.Conflicts != 0 {
		t.Fatalf("shifted revert = %+v, %v", reverted, err)
	}
}

func TestReviewMergeLeavesConflictMarkers(t *testing.T) {
	before := numberedLines(30, nil)
	after := numberedLines(30, map[int]string{15: "agent edit\n"})
	file := RenderReviewFile("a.txt", "a.txt", before, after)
	current := numberedLines(30, map[int]string{15: "user edit\n"})
	merged, err := file.Merge(current, true, "mchanges revert alpha1")
	if err != nil || merged.Conflicts != 1 {
		t.Fatalf("conflict = %+v, %v", merged, err)
	}
	want := "<<<<<<< workspace\nuser edit\n=======\n" + strings.SplitAfter(before, "\n")[14] + ">>>>>>> mchanges revert alpha1\n"
	if !strings.Contains(merged.Content, want) {
		t.Fatalf("markers missing:\n%s", merged.Content)
	}
	// A change within the hunk context, apart from its changed rows, merges cleanly.
	current = numberedLines(30, map[int]string{15: "agent edit\n", 17: "neighbor edit\n"})
	merged, err = file.Merge(current, true, "revert")
	wantClean := numberedLines(30, map[int]string{17: "neighbor edit\n"})
	if err != nil || merged.Conflicts != 0 || merged.Content != wantClean {
		t.Fatalf("adjacent merge = %+v, %v", merged, err)
	}
}

func TestReviewMergeRejectsUnlocatableHunks(t *testing.T) {
	before := numberedLines(30, nil)
	after := numberedLines(30, map[int]string{15: "agent edit\n"})
	file := RenderReviewFile("a.txt", "a.txt", before, after)
	merged, err := file.Merge("unrelated\ncontent\n", true, "revert")
	if err != nil || len(merged.Rejected) != 1 || merged.Content != "unrelated\ncontent\n" ||
		!strings.HasPrefix(merged.Rejected[0], "@@ -12,7 +12,7 @@\n") {
		t.Fatalf("rejected = %+v, %v", merged, err)
	}
}

func TestReviewMergeWholeFileSides(t *testing.T) {
	added := RenderReviewFile("", "new.txt", "", "one\ntwo")
	reverted, err := added.Merge("one\ntwo", true, "revert")
	if err != nil || reverted.Content != "" || reverted.Conflicts != 0 {
		t.Fatalf("revert add = %+v, %v", reverted, err)
	}
	recreated, err := added.Merge("", false, "apply")
	if err != nil || recreated.Content != "one\ntwo" {
		t.Fatalf("apply add = %+v, %v", recreated, err)
	}
	// Both sides created different content at the same path.
	both, err := added.Merge("other\n", false, "apply")
	if err != nil || both.Conflicts != 1 || both.Content != "<<<<<<< workspace\nother\n=======\none\ntwo\n>>>>>>> apply\n" {
		t.Fatalf("add over existing = %+v, %v", both, err)
	}
	if _, err := RenderIncompleteReviewFile("a", "a", "gone").Merge("", true, "revert"); err == nil {
		t.Fatal("incomplete history merged")
	}
}

func TestReviewMergeAnchorsAtFileStartWithEditedContext(t *testing.T) {
	before := numberedLines(10, nil)
	after := numberedLines(10, map[int]string{1: "agent first\n"})
	file := RenderReviewFile("a.txt", "a.txt", before, after)
	// The hunk has no leading context; its nearest trailing context changed.
	current := numberedLines(10, map[int]string{1: "agent first\n", 3: "user third\n"})
	merged, err := file.Merge(current, true, "revert")
	if err != nil || merged.Conflicts != 0 || merged.Content != numberedLines(10, map[int]string{3: "user third\n"}) {
		t.Fatalf("file-start merge = %+v, %v", merged, err)
	}
}

func TestReviewMergeKeepsNewlineBoundaries(t *testing.T) {
	file := RenderReviewFile("a.txt", "a.txt", "a\nb\nc", "a\nb\nc\nd\n")
	// A later edit appended e; the recorded side without a final newline no
	// longer ends the file.
	merged, err := file.Merge("a\nb\nc\nd\ne\n", true, "revert")
	if err != nil || strings.Contains(merged.Content, "\nce\n") || merged.Conflicts == 0 && len(merged.Rejected) == 0 {
		t.Fatalf("newline merge = %+v, %v", merged, err)
	}
}

func TestReviewMergeIgnoresDuplicateBlocks(t *testing.T) {
	block := "func f() {\n\tx := 1\n\ty := 2\n\tw := 0\n\tz := 3\n\treturn\n}\n"
	before := block + "\n" + block
	after := block + "\n" + strings.Replace(block, "y := 2", "y := 99", 1)
	file := RenderReviewFile("a.go", "a.go", before, after)
	// A later edit changed the reverted hunk's context, and an identical copy
	// of the requested side remains above it.
	current := block + "\n" + strings.Replace(strings.Replace(block, "y := 2", "y := 99", 1), "z := 3", "z := 4", 1)
	merged, err := file.Merge(current, true, "revert")
	want := block + "\n" + strings.Replace(block, "z := 3", "z := 4", 1)
	if err != nil || merged.Satisfied != 0 || merged.Content != want {
		t.Fatalf("duplicate block merge = %+v, %v", merged, err)
	}
}
