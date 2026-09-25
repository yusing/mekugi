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
	return RenderReviewFile("file.txt", "file.txt", before, after)
}

func composedText(c *ReviewComposition) string {
	var out strings.Builder
	for _, file := range c.FilesWithHighlights() {
		out.WriteString(file.ReviewFile.UnifiedDiff())
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
	if err := composition.ApplyWithHighlight(file, false, false); err == nil {
		t.Fatal("complete capture accepted an unexplained line shift")
	}
}

func TestReviewCompositionNewlineAndPathChanges(t *testing.T) {
	var c ReviewComposition
	add := RenderReviewFile("", "a.txt", "", "a\r\nb")
	if err := c.ApplyWithHighlight(add, false, false); err != nil {
		t.Fatal(err)
	}
	move := RenderReviewFile("a.txt", "b.txt", "a\r\nb", "a\nb\n")
	if err := c.ApplyWithHighlight(move, false, false); err != nil {
		t.Fatal(err)
	}
	text := composedText(&c)
	if !strings.Contains(text, "--- /dev/null\n+++ \"b.txt\"") || !strings.Contains(text, "+a\n+b\n") {
		t.Fatalf("move/add: %q", text)
	}
	del := RenderReviewFile("b.txt", "", "a\nb\n", "")
	if err := c.ApplyWithHighlight(del, false, false); err != nil {
		t.Fatal(err)
	}
	if len(c.FilesWithHighlights()) != 0 {
		t.Fatalf("add/delete did not cancel: %q", composedText(&c))
	}

	c = ReviewComposition{}
	initial := composeCapture("a\r\nb", "a\nb\n")
	if err := c.ApplyWithHighlight(initial, false, false); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyWithHighlight(composeCapture("a\nb\n", "a\r\nb"), false, false); err != nil {
		t.Fatal(err)
	}
	if len(c.FilesWithHighlights()) != 0 {
		t.Fatal("line-ending revert remains visible")
	}
}

func TestReviewCompositionValidationIsAtomic(t *testing.T) {
	var c ReviewComposition
	if err := c.ApplyWithHighlight(composeCapture("a\nb\nc\n", "a\nB\nc\n"), false, false); err != nil {
		t.Fatal(err)
	}
	before := c
	for _, file := range []ReviewFile{
		composeCapture("a\nnot-B\nc\n", "a\nC\nc\n"),
		{BeforePath: "file.txt", AfterPath: "file.txt", Diff: "--- old\n+++ new\n@@ -1,9 +1,1 @@\n-a\n+b\n"},
	} {
		if err := c.ApplyWithHighlight(file, false, false); err == nil {
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
	for _, r := range slices.Backward(c.regions) {
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
		if err := c.ApplyWithHighlight(composeCapture(strings.Join(current, ""), strings.Join(next, "")), false, false); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		checkComposition(t, &c, base, next)
		if slices.Equal(next, base) && len(c.FilesWithHighlights()) != 0 {
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
		if err := c.ApplyWithHighlight(composeCapture(strings.Join(current, ""), strings.Join(next, "")), false, false); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		checkComposition(t, &c, base, next)
		if slices.Equal(next, base) && len(c.FilesWithHighlights()) != 0 {
			t.Fatalf("step %d: revert remains", step)
		}
		current = next
	}
}

func TestReviewCompositionContextCoordinatesAppearOnce(t *testing.T) {
	for gap := 0; gap <= 8; gap++ {
		for _, replacement := range []string{"FIRST\n", "FIRST\nINSERTED\n", ""} {
			t.Run(fmt.Sprintf("gap%d/%q", gap, replacement), func(t *testing.T) {
				base := "head\nfirst\n" + strings.Repeat("same context\n", gap) + "second\ntail\n"
				first := strings.Replace(base, "first\n", replacement, 1)
				last := strings.Replace(first, "second\n", "SECOND\n", 1)
				var c ReviewComposition
				if err := c.ApplyWithHighlight(composeCapture(base, first), false, false); err != nil {
					t.Fatal(err)
				}
				if err := c.ApplyWithHighlight(composeCapture(first, last), false, true); err != nil {
					t.Fatal(err)
				}
				oldSeen, newSeen := map[int]bool{}, map[int]bool{}
				added, removed := 0, 0
				for _, file := range c.FilesWithHighlights() {
					hunks, err := file.Hunks()
					if err != nil {
						t.Fatal(err)
					}
					for _, hunk := range hunks {
						old, next := hunk.BeforeStart, hunk.AfterStart
						for _, row := range hunk.Rows {
							if row.Kind != '+' {
								if oldSeen[old] {
									t.Fatalf("old coordinate %d displayed twice", old)
								}
								oldSeen[old] = true
								old++
							}
							if row.Kind != '-' {
								if newSeen[next] {
									t.Fatalf("new coordinate %d displayed twice", next)
								}
								newSeen[next] = true
								next++
							}
							if row.Kind == '+' {
								added++
							} else if row.Kind == '-' {
								removed++
							}
						}
					}
				}
				if added != strings.Count(replacement, "\n")+1 || removed != 2 {
					t.Fatalf("changed source lost: +%d -%d", added, removed)
				}
			})
		}
	}
}
