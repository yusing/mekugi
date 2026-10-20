package outputdedupe

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func testBlock(label string, rows int) string {
	var text strings.Builder
	for row := range rows {
		fmt.Fprintf(&text, "%s-%d %s\n", label, row, strings.Repeat("x", 92))
	}
	return text.String()
}

func TestMatcherRuns(t *testing.T) {
	a, b, c := testBlock("a", 3), testBlock("b", 3), testBlock("c", 3)
	longLine := strings.Repeat("single-line", 20) + "\n"
	seven := "abcdefg" + strings.Repeat(" ", Threshold)
	eight := "abcdefgh" + strings.Repeat(" ", Threshold)
	tests := []struct {
		name, earlier, text string
		want                []Span
	}{
		{"exact", b, b, []Span{{0, len(b), 17, 1, 3, true}}},
		{"contained", a + b + c, b, []Span{{0, len(b), 17, 4, 6, false}}},
		{"containing", b, a + b + c, []Span{{len(a), len(a) + len(b), 17, 1, 3, true}}},
		{"reordered command", a + b + c, "changed c\n" + b, []Span{{len("changed c\n"), len("changed c\n") + len(b), 17, 4, 6, false}}},
		{"edited region", a + b + c, a + "edited\n" + c, []Span{{0, len(a), 17, 1, 3, false}, {len(a) + len("edited\n"), len(a) + len("edited\n") + len(c), 17, 7, 9, false}}},
		{"different diff headers", "mchanges header\n" + b, "diff --git a/f b/f\nindex abc..def\n@@ -1,3 +1,3 @@\n" + b, []Span{{len("diff --git a/f b/f\nindex abc..def\n@@ -1,3 +1,3 @@\n"), len("diff --git a/f b/f\nindex abc..def\n@@ -1,3 +1,3 @@\n") + len(b), 17, 2, 4, false}}},
		{"one line inside longer unit", a + longLine + c, longLine, []Span{{0, len(longLine), 17, 4, 4, false}}},
		{"two lines inside longer unit", a + testBlock("b", 2) + c, testBlock("b", 2), []Span{{0, len(testBlock("b", 2)), 17, 4, 5, false}}},
		{"unterminated final line matches middle", a + b + c, strings.TrimSuffix(b, "\n"), []Span{{0, len(b) - 1, 17, 4, 6, false}}},
		{"unterminated source matches terminated output", strings.TrimSuffix(b, "\n"), b, []Span{{0, len(b), 17, 1, 3, true}}},
		{"below threshold", longLine, longLine[:Threshold-1], nil},
		{"line boundaries", longLine, "prefix " + longLine, nil},
		{"CRLF remains distinct", b, strings.ReplaceAll(b, "\n", "\r\n"), nil},
		{"blank and brace lines alone", strings.Repeat(" \t\n{\n}\n", 40), strings.Repeat(" \t\n{\n}\n", 40), nil},
		{"seven nonspace bytes are not a seed", seven, seven, nil},
		{"eight nonspace bytes are a seed", eight, eight, []Span{{0, len(eight), 17, 1, 1, true}}},
		{"trivial rows extend a seeded run", "{\n" + b + "\n}\n", "{\n" + b + "\n}\n", []Span{{0, len("{\n" + b + "\n}\n"), 17, 1, 6, true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := New()
			defer idx.Close()
			idx.Add(17, idx.Project(tt.earlier))
			for range 2 { // Project is read-only and repeatable.
				if got := idx.Project(tt.text).Spans; !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("spans = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

func TestMatcherThreshold(t *testing.T) {
	idx := New()
	defer idx.Close()
	short := strings.Repeat("s", Threshold-1)
	idx.Add(1, idx.Project(short))
	if got := idx.Project(short + "\n" + testBlock("tail", 3)).Spans; len(got) != 0 {
		t.Fatalf("below-threshold source was indexed: %+v", got)
	}
	exact := strings.Repeat("e", Threshold)
	idx.Add(2, idx.Project(exact))
	if got, want := idx.Project(exact).Spans, []Span{{0, Threshold, 2, 1, 1, true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("threshold boundary: got %+v, want %+v", got, want)
	}
}

func TestMatcherVisibleRows(t *testing.T) {
	idx := New()
	defer idx.Close()
	a, b, c := testBlock("a", 3), testBlock("b", 3), testBlock("c", 3)
	idx.Add(1, idx.Project(b))
	projected := idx.Project(a + b + c)
	idx.Add(2, projected)
	// The replaced b is not indexed under source 2. Its marker occupies row 4,
	// so c begins at delivered row 5, rather than original row 7.
	want := []Span{{0, len(a), 2, 1, 3, false}, {len(a), len(a) + len(b), 1, 1, 3, true}, {len(a) + len(b), len(a) + len(b) + len(c), 2, 5, 7, false}}
	if got := idx.Project(a + b + c).Spans; !reflect.DeepEqual(got, want) {
		t.Fatalf("visible references = %+v, want %+v", got, want)
	}
	// Full repeats contribute no new reference targets.
	idx.Add(3, idx.Project(b))
	if got := idx.Project(b).Spans; len(got) != 1 || got[0].Source != 1 {
		t.Fatalf("second-hop reference: %+v", got)
	}
}

func TestMatcherWindowEviction(t *testing.T) {
	idx := New()
	defer idx.Close()
	old := testBlock("old", 3)
	idx.Add(1, idx.Project(old))
	// A full-window line evicts all earlier content, but remains referenceable.
	full := strings.Repeat("w", WindowBytes)
	idx.Add(2, idx.Project(full))
	if got := idx.Project(old).Spans; len(got) != 0 {
		t.Fatalf("evicted output matched: %+v", got)
	}
	if got := idx.Project(full).Spans; len(got) != 1 || got[0].Source != 2 || !got[0].Whole {
		t.Fatalf("full window did not match: %+v", got)
	}
	idx.Add(3, idx.Project(strings.Repeat("z", WindowBytes+1)))
	if got := idx.Project(strings.Repeat("z", WindowBytes+1)).Spans; len(got) != 0 {
		t.Fatalf("oversized line was retained: %+v", got)
	}
	idx.Add(4, idx.Project(old))
	idx.Add(5, idx.Project(strings.Repeat("\n", WindowBytes)))
	if got := idx.Project(old).Spans; len(got) != 0 {
		t.Fatalf("seedless text did not advance the window: %+v", got)
	}
	// A partial eviction keeps whole suffix rows with their original row numbers.
	idx.Close()
	idx = New()
	defer idx.Close()
	text := strings.Repeat("p", WindowBytes-10) + "\n" + old
	idx.Add(6, idx.Project(text))
	want := []Span{{0, len(old), 6, 2, 4, false}}
	if got := idx.Project(old).Spans; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained suffix = %+v, want %+v", got, want)
	}
}

func deterministicSpans() []Span {
	idx := New()
	defer idx.Close()
	b := testBlock("shared", 3)
	// Prepare both verbatim candidates before adding either, so equal candidates
	// from distinct sources exercise the tie-breaker rather than deduping history.
	older := idx.Project("old heading\n" + b)
	newer := idx.Project("new heading\n" + b + "tail\n")
	idx.Add(99, older)
	idx.Add(1, newer)
	tie := idx.Project(b).Spans
	// A longer run beats a newer, shorter candidate.
	idx.Close()
	idx = New()
	defer idx.Close()
	longer := idx.Project(b + testBlock("suffix", 3))
	shorter := idx.Project(b)
	idx.Add(7, longer)
	idx.Add(8, shorter)
	return append(tie, idx.Project(b+testBlock("suffix", 3)).Spans...)
}

func TestMatcherDeterministicAcrossProcesses(t *testing.T) {
	if os.Getenv("MEKUGI_OUTPUTDEDUPE_TEST_CHILD") == "1" {
		fmt.Print(deterministicSpans())
		os.Exit(0)
	}
	b := testBlock("shared", 3)
	want := []Span{{0, len(b), 1, 2, 4, false}, {0, len(b) + len(testBlock("suffix", 3)), 7, 1, 6, true}}
	if got := deterministicSpans(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tie/longest selection = %+v, want %+v", got, want)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for range 4 { // Each process independently randomizes maphash seeds.
		cmd := exec.Command(executable, "-test.run=^TestMatcherDeterministicAcrossProcesses$")
		cmd.Env = append(os.Environ(), "MEKUGI_OUTPUTDEDUPE_TEST_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child process: %v: %s", err, output)
		}
		if string(output) != fmt.Sprint(want) {
			t.Fatalf("fresh-process spans = %s, want %v", output, want)
		}
	}
}

func TestMatcherSeedlessAllocations(t *testing.T) {
	idx := New()
	defer idx.Close()
	text := strings.Repeat("\n", WindowBytes)
	allocations := testing.AllocsPerRun(10, func() {
		projection := idx.Project(text)
		if len(projection.Spans) != 0 {
			t.Fatal("seedless text matched")
		}
		idx.Add(1, projection)
	})
	if allocations != 0 {
		t.Fatalf("seedless scan allocated %g objects", allocations)
	}
}

func TestMatcherClose(t *testing.T) {
	idx := New()
	defer idx.Close()
	text := testBlock("pooled", 32)
	projection := idx.Project(text)
	idx.Add(1, projection)
	idx.Close()
	idx.Close()
	if got := idx.Project(text).Spans; len(got) != 0 {
		t.Fatalf("closed index retained references: %+v", got)
	}
	// A projection retains host text, not borrowed index buffers. It can be
	// added after release.
	idx.Add(3, projection)
	want := []Span{{0, len(text), 3, 1, 32, true}}
	if got := idx.Project(text).Spans; !reflect.DeepEqual(got, want) {
		t.Fatalf("projection after release = %+v, want %+v", got, want)
	}
}

func TestMatcherFragmentedWindow(t *testing.T) {
	idx := New()
	defer idx.Close()
	for source, text := range benchmarkFragmentedHistory() {
		idx.Add(source, idx.Project(text))
	}
	capacity := 0
	for _, p := range idx.pieces {
		capacity += cap(p.text) + cap(p.seeds)
	}
	// Only about 86 KiB of text and seeds is live. Applying a 2 KiB pool tier
	// to each tiny tail used to retain over 16 MiB for this same visible input.
	if capacity > WindowBytes {
		t.Fatalf("fragmented window retained %d buffer bytes", capacity)
	}
}
