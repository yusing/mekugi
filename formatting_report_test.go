package mekugi

import (
	"fmt"
	"strings"
	"testing"
)

func TestFormattingReferences(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		want                []string
	}{
		{"single", "var x=1\n", "var x = 1\n", []string{row(1, "var x=1") + " -> " + row(1, "var x = 1")}},
		{"block", "func f(){println(1);println(2)}\nvar x = 1\n", "func f() {\n\tprintln(1)\n\tprintln(2)\n}\nvar x = 1\n",
			[]string{"Formatted 1-4\n", row(2, "\tprintln(1)") + " \\tprintln(1)\n", "shift 2-2 -> 5-5 (hashes unchanged)"}},
		{"delete", "a\nb\nc\n", "a\nc\n", []string{"Formatted removed 2-2 before final line 2", "shift 3-3 -> 2-2"}},
		{"insert", "a\nc\n", "a\nb\nc\n", []string{"Formatted 2-2", row(2, "b") + " b\n", "shift 2-2 -> 3-3"}},
		{"terminators", "a\rb\r\nc\n", "a\nB\nc\n", []string{row(2, "b") + " -> " + row(2, "B")}},
		{"long", "a\nb\n", strings.Repeat("x", 100) + "\nc\n", []string{row(1, strings.Repeat("x", 100)) + " " + strings.Repeat("x", 100)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var report strings.Builder
			writeFormattingReferences(&report, tc.before, tc.after)
			for _, want := range tc.want {
				if !strings.Contains(report.String(), want) {
					t.Fatalf("report %q lacks %q", report.String(), want)
				}
			}
		})
	}
}

func TestFormattingReferencesApplyAndReuse(t *testing.T) {
	root := t.TempDir()
	source := "package p\n\nfunc f(){println(1);println(2)}\n\nvar z=1\n"
	writeTestFile(t, root, "f.go", source, 0o644)
	edits := []FileEdit{{Path: "f.go", Script: `type "package p" "package q"`}}
	translated, err := TranslateForHostAt(t.Context(), root, edits, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyForHostAt(t.Context(), root, edits, "")
	if err != nil {
		t.Fatal(err)
	}
	if translated.Report != result.Report {
		t.Fatalf("translation and apply reports differ:\n%s\n%s", translated.Report, result.Report)
	}
	final := readTestFile(t, root, "f.go")
	if !strings.Contains(result.Report, "format (pre-format -> final)") {
		t.Fatal(result.Report)
	}
	// Feed every emitted block row and mapping destination through the edit
	// consumer, including rows outside the authored edit.
	count := 0
	for _, line := range strings.Split(result.Report[strings.Index(result.Report, "format"):], "\n") {
		target := line
		if _, right, ok := strings.Cut(line, " -> "); ok {
			target = right
		}
		var number int
		var hash string
		if _, err := fmt.Sscanf(target, "%d:%s", &number, &hash); err != nil || len(hash) != 4 {
			continue
		}
		lines := logicalLines(final)
		content := lineContent(final, lines[number-1])
		edits := []FileEdit{{Path: "f.go", Script: fmt.Sprintf("type %d:%s %q", number, hash, content)}}
		if _, err := applyForHostAtTest(t, root, edits, ""); err != nil {
			t.Fatalf("reuse %q: %v", target, err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no reusable references")
	}
}

func TestFormattingReferencesAbsentWithoutFormatting(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "f.go", "package p\n", 0o644)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "f.go", Script: `type "package p" "package q"`}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Report, "format") {
		t.Fatal(result.Report)
	}
}

func TestFormattingBlockEscapesControls(t *testing.T) {
	root := t.TempDir()
	source := "package p\nfunc f(){\nprintln(`\x1b[31m`);println(2)\n}\n"
	writeTestFile(t, root, "f.go", source, 0o644)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "f.go", Script: `type "package p" "package q"`}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(result.Report, '\x1b') {
		t.Fatalf("terminal escape survived in report: %q", result.Report)
	}
	start := strings.Index(result.Report, "format")
	if start < 0 {
		t.Fatal(result.Report)
	}
	want := row(4, "\tprintln(`\x1b[31m`)") + " \\tprintln(`\\x1b[31m`)\n"
	if !strings.Contains(result.Report[start:], want) {
		t.Fatalf("formatter block lacks escaped full row %q: %q", want, result.Report[start:])
	}
}

func TestFormattingShiftReferencesWithRepeatedRows(t *testing.T) {
	root := t.TempDir()
	source := "package p\nfunc f(){println(1);println(2)}\nfunc g() {\n\tprintln(1)\n}\n"
	writeTestFile(t, root, "f.go", source, 0o644)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "f.go", Script: `type "package p" "package q"`}}, "")
	if err != nil {
		t.Fatal(err)
	}
	before := logicalLines(source)
	foundRepeated := false
	for _, line := range strings.Split(result.Report, "\n") {
		var oldStart, oldEnd, start, end int
		if _, err := fmt.Sscanf(line, "shift %d-%d -> %d-%d (hashes unchanged)", &oldStart, &oldEnd, &start, &end); err != nil {
			continue
		}
		if oldEnd-oldStart != end-start {
			t.Fatalf("invalid shift: %s", line)
		}
		for old := oldStart; old <= oldEnd; old++ {
			content := lineContent(source, before[old-1])
			target := row(start+old-oldStart, content)
			if _, err := applyForHostAtTest(t, root, []FileEdit{{Path: "f.go", Script: "type " + target + " " + fmt.Sprintf("%q", content)}}, ""); err != nil {
				t.Fatalf("shifted target %s: %v", target, err)
			}
			foundRepeated = foundRepeated || content == "\tprintln(1)"
		}
	}
	if !foundRepeated {
		t.Fatalf("no shifted repeated row: %s", result.Report)
	}
}
