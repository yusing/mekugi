package mekugi

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func composeCapture(before, after string) ReviewFile {
	return reviewFiles([]change{{kind: changeUpdate, originalPath: "file.txt", path: "file.txt", original: before, content: after}})[0]
}

func composedText(c *ReviewComposition) string {
	var out strings.Builder
	for _, file := range c.Files() {
		out.WriteString(file.UnifiedDiff())
	}
	return out.String()
}

func TestReviewHunkGeometry(t *testing.T) {
	file := ReviewFile{Diff: "--- x\n+++ x\n@@ -1,2 +1,3 @@\n a\n+insert\n b\n@@ -20 +21 @@\n-old\n+new\n"}
	hunks, err := file.Hunks()
	if err != nil || len(hunks) != 2 {
		t.Fatalf("hunks: %#v %v", hunks, err)
	}
	if hunks[0].AfterStart != 0 || len(hunks[0].Rows) != 3 || hunks[0].ChangedStart != 1 ||
		hunks[1].AfterStart != 20 || hunks[1].ChangedStart != 20 {
		t.Fatalf("wrong current-source geometry: %#v", hunks)
	}
}

func TestReviewHunkRowsPreserveSource(t *testing.T) {
	file := composeCapture("context\r\nbefore", "context\r\nafter\n")
	hunks, err := file.Hunks()
	if err != nil || len(hunks) != 1 {
		t.Fatalf("hunks: %#v %v", hunks, err)
	}
	want := []ReviewRow{{' ', "context\r\n"}, {'-', "before"}, {'+', "after\n"}}
	if hunks[0].BeforeStart != 0 || !slices.Equal(hunks[0].Rows, want) {
		t.Fatalf("source rows: %#v", hunks[0])
	}
}

func TestReviewHunkGeometryAfterHiddenLineShifts(t *testing.T) {
	file := ReviewFile{Diff: "--- x\n+++ x\n@@ -20 +21 @@\n-old\n+new\n"}
	hunks, err := file.Hunks()
	if err != nil || len(hunks) != 1 || hunks[0].ChangedStart != 20 {
		t.Fatalf("partial composed geometry: %#v %v", hunks, err)
	}
}

func TestReviewHunkValidation(t *testing.T) {
	for _, diff := range []string{
		"@@ -1 +1 @@ extra\n-a\n+b\n",
		"@@ 1 +1 @@\n-a\n+b\n",
		"@@ -0 +1 @@\n-a\n+b\n",
		"@@ -1,1048577 +1 @@\n-a\n+b\n",
		"@@ -1 +1 @@\n?invalid\n",
		"@@ -1,2 +1 @@\n-a\n+b\n",
		"@@ -1 +1 @@\n\\ No newline at end of file\n-a\n+b\n",
		"@@ -1 +1 @@\n-a\n+b\n@@ -1 +1 @@\n-c\n+d\n",
	} {
		t.Run(diff, func(t *testing.T) {
			if hunks, err := (ReviewFile{Diff: diff}).Hunks(); err == nil || hunks != nil {
				t.Fatalf("invalid capture returned hunks: %+v, %v", hunks, err)
			}
		})
	}
	file := ReviewFile{BeforePath: "x", AfterPath: "x", Diff: "@@ -1 +2 @@\n-a\n+b\n"}
	var composition ReviewComposition
	if err := composition.Apply(file, false); err == nil {
		t.Fatal("complete capture accepted an unexplained line shift")
	}
}

func TestReviewCompositionFlushOverlapAndRevert(t *testing.T) {
	base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	a := strings.Replace(base, "two\n", "TWO\n", 1)
	var c ReviewComposition
	if err := c.Apply(composeCapture(base, a), false); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	if len(c.Files()) != 0 {
		t.Fatal("flush retained visible diff")
	}
	b := strings.Replace(a, "nine\n", "NINE\n", 1)
	if err := c.Apply(composeCapture(a, b), false); err != nil {
		t.Fatal(err)
	}
	if text := composedText(&c); strings.Contains(text, "+TWO\n") || !strings.Contains(text, "+NINE\n") {
		t.Fatalf("unrelated reviewed region revived: %q", text)
	}
	c.Flush()
	fixed := strings.Replace(b, "TWO\n", "Two fixed\n", 1)
	if err := c.Apply(composeCapture(b, fixed), false); err != nil {
		t.Fatal(err)
	}
	if text := composedText(&c); !strings.Contains(text, "-two\n+Two fixed\n") || strings.Contains(text, "+NINE\n") || strings.Contains(text, "-TWO\n") {
		t.Fatalf("did not revive original baseline: %q", text)
	}
	reverted := strings.Replace(fixed, "Two fixed\n", "two\n", 1)
	if err := c.Apply(composeCapture(fixed, reverted), false); err != nil {
		t.Fatal(err)
	}
	if len(c.Files()) != 0 {
		t.Fatalf("reverted hunk remains: %q", composedText(&c))
	}
}

func TestReviewCompositionFlushAdjacentEdits(t *testing.T) {
	for _, pair := range [][2]string{
		{"a\nB\n", "A\nB\n"},
		{"A\nb\n", "A\nB\n"},
	} {
		var c ReviewComposition
		if err := c.Apply(composeCapture("a\nb\n", pair[0]), false); err != nil {
			t.Fatal(err)
		}
		c.Flush()
		if err := c.Apply(composeCapture(pair[0], pair[1]), false); err != nil {
			t.Fatal(err)
		}
		reviewed, fresh := "+B\n", "+A\n"
		if pair[0] == "A\nb\n" {
			reviewed, fresh = fresh, reviewed
		}
		if text := composedText(&c); strings.Contains(text, reviewed) || !strings.Contains(text, fresh) {
			t.Fatalf("adjacent edit revived reviewed change: %q", text)
		}
		checkComposition(t, &c, reviewLines("a\nb\n"), reviewLines(pair[1]))
		if err := c.Apply(composeCapture(pair[1], "a\nb\n"), false); err != nil {
			t.Fatal(err)
		}
		if len(c.Files()) != 0 {
			t.Fatal("full revert retained adjacent changes")
		}
	}
}

func TestReviewCompositionFlushAdjacentInsertionsAndDeletions(t *testing.T) {
	for _, tc := range []struct{ base, first, next, hidden, visible string }{
		{"a\nb\nc\n", "a\nc\n", "c\n", "-b\n", "-a\n"},
		{"a\nb\nc\n", "a\nc\n", "a\n", "-b\n", "-c\n"},
		{"a\nc\n", "a\nb\nc\n", "a\nb\nnew\nc\n", "+b\n", "+new\n"},
		{"a\nc\n", "a\nb\nc\n", "a\nnew\nb\nc\n", "+b\n", "+new\n"},
	} {
		var c ReviewComposition
		if err := c.Apply(composeCapture(tc.base, tc.first), false); err != nil {
			t.Fatal(err)
		}
		c.Flush()
		if err := c.Apply(composeCapture(tc.first, tc.next), false); err != nil {
			t.Fatal(err)
		}
		if text := composedText(&c); strings.Contains(text, tc.hidden) || !strings.Contains(text, tc.visible) {
			t.Fatalf("adjacent change revived reviewed content: %q", text)
		}
		checkComposition(t, &c, reviewLines(tc.base), reviewLines(tc.next))
		if err := c.Apply(composeCapture(tc.next, tc.base), false); err != nil {
			t.Fatal(err)
		}
		if text := composedText(&c); text != "" {
			t.Fatalf("revert retained changes: %q", text)
		}
	}
}

func TestReviewCompositionFlushRepeatedLineRevert(t *testing.T) {
	base, changed := "a\nb\na\na\n", "b\na\nb\nb\na\n"
	var c ReviewComposition
	if err := c.Apply(composeCapture(base, changed), false); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	if err := c.Apply(composeCapture(changed, base), false); err != nil {
		t.Fatal(err)
	}
	if text := composedText(&c); text != "" {
		t.Fatalf("full repeated-line revert remains visible: %q", text)
	}
}

func TestReviewCompositionPartialRevertAndLineShifts(t *testing.T) {
	base := "a\nb\nc\nd\ne\nf\ng\n"
	a := "a\nB\nC\nd\ne\nf\ng\n"
	var c ReviewComposition
	for i, pair := range [][2]string{{base, a}, {a, "prefix\n" + a}, {"prefix\n" + a, "prefix\na\nb\nC\nd\ne\nf\ng\n"}} {
		if err := c.Apply(composeCapture(pair[0], pair[1]), false); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		c.Flush()
	}
	next := "prefix\na\nb\nFixed C\nd\ne\nf\ng\n"
	if err := c.Apply(composeCapture("prefix\na\nb\nC\nd\ne\nf\ng\n", next), false); err != nil {
		t.Fatal(err)
	}
	text := composedText(&c)
	if !strings.Contains(text, "-c\n+Fixed C\n") || strings.Contains(text, "+prefix\n") || strings.Contains(text, "+B\n") {
		t.Fatalf("partial revert or shifted overlap lost: %q", text)
	}
	checkComposition(t, &c, reviewLines(base), reviewLines(next))
}

func TestReviewCompositionNewlineAndPathChanges(t *testing.T) {
	var c ReviewComposition
	add := reviewFiles([]change{{kind: changeAdd, path: "a.txt", content: "a\r\nb"}})[0]
	if err := c.Apply(add, false); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	move := reviewFiles([]change{{kind: changeUpdate, originalPath: "a.txt", path: "b.txt", original: "a\r\nb", content: "a\nb\n"}})[0]
	if err := c.Apply(move, false); err != nil {
		t.Fatal(err)
	}
	text := composedText(&c)
	if !strings.Contains(text, "--- /dev/null\n+++ \"b.txt\"") || !strings.Contains(text, "+a\n+b\n") {
		t.Fatalf("move/add: %q", text)
	}
	del := reviewFiles([]change{{kind: changeDelete, originalPath: "b.txt", original: "a\nb\n"}})[0]
	if err := c.Apply(del, false); err != nil {
		t.Fatal(err)
	}
	if len(c.Files()) != 0 {
		t.Fatalf("add/delete did not cancel: %q", composedText(&c))
	}

	c = ReviewComposition{}
	initial := composeCapture("a\r\nb", "a\nb\n")
	if err := c.Apply(initial, false); err != nil {
		t.Fatal(err)
	}
	c.Flush()
	if err := c.Apply(composeCapture("a\nb\n", "a\r\nb"), false); err != nil {
		t.Fatal(err)
	}
	if len(c.Files()) != 0 {
		t.Fatal("line-ending revert remains visible")
	}
}

func TestReviewCompositionValidationIsAtomic(t *testing.T) {
	var c ReviewComposition
	if err := c.Apply(composeCapture("a\nb\nc\n", "a\nB\nc\n"), false); err != nil {
		t.Fatal(err)
	}
	before := c
	for _, file := range []ReviewFile{
		composeCapture("a\nnot-B\nc\n", "a\nC\nc\n"),
		{BeforePath: "file.txt", AfterPath: "file.txt", Diff: "--- old\n+++ new\n@@ -1,9 +1,1 @@\n-a\n+b\n"},
	} {
		if err := c.Apply(file, false); err == nil {
			t.Fatal("invalid chain accepted")
		}
		if !reflect.DeepEqual(c, before) {
			t.Fatal("failed apply changed composition")
		}
	}
}

func checkComposition(t *testing.T, c *ReviewComposition, base, current []string) {
	t.Helper()
	for position, line := range c.original {
		if position < 0 || position >= len(base) || base[position] != line {
			t.Fatalf("original context drift at %d: %q", position, line)
		}
	}
	result := slices.Clone(base)
	for i := len(c.regions) - 1; i >= 0; i-- {
		r := c.regions[i]
		if r.beforeStart < 0 || r.beforeStart+len(r.before) > len(base) ||
			!slices.Equal(base[r.beforeStart:r.beforeStart+len(r.before)], r.before) {
			t.Fatalf("original region mismatch: %#v base=%q", r, base)
		}
		if r.afterStart < 0 || r.afterStart+len(r.after) > len(current) ||
			!slices.Equal(current[r.afterStart:r.afterStart+len(r.after)], r.after) {
			t.Fatalf("current region mismatch: %#v current=%q", r, current)
		}
		if r.beforeStart+len(r.before) > len(result) {
			t.Fatalf("overlapping original regions: %#v", c.regions)
		}
		result = slices.Replace(result, r.beforeStart, r.beforeStart+len(r.before), r.after...)
	}
	if !slices.Equal(result, current) {
		t.Fatalf("composed %q; want %q", result, current)
	}
}

func TestReviewCompositionRandomSequentialEdits(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 31))
	base := reviewLines(strings.Repeat("same\n", 12) + "end\n")
	current := slices.Clone(base)
	var c ReviewComposition
	for step := range 300 {
		next := slices.Clone(current)
		start := rng.IntN(len(next) + 1)
		end := min(len(next), start+rng.IntN(4))
		var added []string
		for range rng.IntN(4) {
			added = append(added, fmt.Sprintf("%d\n", rng.IntN(5)))
		}
		next = slices.Replace(next, start, end, added...)
		if step%31 == 30 {
			next = slices.Clone(base)
		}
		if err := c.Apply(composeCapture(strings.Join(current, ""), strings.Join(next, "")), false); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		checkComposition(t, &c, base, next)
		if step%7 == 0 {
			c.Flush()
		}
		if slices.Equal(next, base) && len(c.Files()) != 0 {
			t.Fatalf("step %d: complete revert remains", step)
		}
		current = next
	}
}

func TestReviewCompositionRepeatedLinesLarge(t *testing.T) {
	base := reviewLines(strings.Repeat("same\n", 250) + "end\n")
	rng := rand.New(rand.NewPCG(19, 73))
	current := slices.Clone(base)
	var c ReviewComposition
	for step := range 100 {
		next := slices.Clone(current)
		start := rng.IntN(len(next) + 1)
		end := min(len(next), start+rng.IntN(5))
		next = slices.Replace(next, start, end, fmt.Sprintf("edit %d\n", step))
		if step%17 == 16 {
			next = slices.Clone(base)
		}
		if err := c.Apply(composeCapture(strings.Join(current, ""), strings.Join(next, "")), false); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		checkComposition(t, &c, base, next)
		if step%5 == 0 {
			c.Flush()
		}
		if slices.Equal(next, base) && len(c.Files()) != 0 {
			t.Fatalf("step %d: revert remains", step)
		}
		current = next
	}
}
