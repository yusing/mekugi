package mekugi

import (
	"strings"
	"testing"
)

func repairFor(t *testing.T, content, script string) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", content, 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: script}}
	result, err := translateForHostAtTest(t, root, edits, "")
	if err == nil {
		t.Fatalf("script unexpectedly succeeded: %q", script)
	}
	_, repair, _ := strings.Cut(result.Diagnostic, "\n")
	return repair
}

func TestMekugi2RepairContextForIncompleteTextTarget(t *testing.T) {
	content := "none\ntarget\nnone\n"
	anchor := row(1, "none")
	repair := repairFor(t, content, "type "+anchor+` "target" 2 "replacement"`)
	for _, want := range []string{
		"found 1 of 2 requested matches at or after line 1",
		"apply that prerequisite, reread, and submit a later invocation",
		"matching lines: 2",
		"1:" + hashLine("none") + " none",
	} {
		if !strings.Contains(repair, want) {
			t.Fatalf("repair lacks %q:\n%s", want, repair)
		}
	}
}

func TestMekugi2RepairContextForIncompleteUnanchoredTextTarget(t *testing.T) {
	repair := repairFor(t, "none\ntarget\nnone\n", `type "target" 2 "replacement"`)
	for _, want := range []string{
		"found 1 of 2 requested matches in immutable baseline",
		"matching lines: 2",
	} {
		if !strings.Contains(repair, want) {
			t.Fatalf("repair lacks %q:\n%s", want, repair)
		}
	}
}

func TestMekugi2RepairContextForStaleRow(t *testing.T) {
	repair := repairFor(t, "alpha\nbeta\n", `type 2:0000 "B"`)
	if !strings.Contains(repair, "2:"+hashLine("beta")+" beta") {
		t.Fatalf("repair = %q", repair)
	}
}

func TestMekugi2RepairContextForStaleRangeEnd(t *testing.T) {
	content := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n"
	repair := repairFor(t, content, "type "+row(1, "one")+"..7:0000 \"\"")
	if !strings.Contains(repair, "7:"+hashLine("seven")+" seven") {
		t.Fatalf("repair = %q", repair)
	}
}

func TestMekugi2MissingRowDoesNotGuessRepairContext(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\n", 0o644)
	result, err := translateForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: `type 9:0000 "B"`}}, "")
	if err == nil || !strings.Contains(result.Diagnostic, "row-missing") {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if strings.Count(result.Diagnostic, "\n") != 1 {
		t.Fatalf("missing row emitted guessed repair context: %q", result.Diagnostic)
	}
}

func TestMekugi2RepairContextForEditConflict(t *testing.T) {
	content := "alpha\nbeta\n"
	script := "type " + row(1, "alpha") + ` "A"` + "\ntype " + row(1, "alpha") + ` ""`
	repair := repairFor(t, content, script)
	for _, want := range []string{
		"baseline content conflicts with an earlier mutation",
		"command 1 (type) line 1",
		"1:" + hashLine("alpha") + " alpha",
	} {
		if !strings.Contains(repair, want) {
			t.Fatalf("repair lacks %q:\n%s", want, repair)
		}
	}
}

func TestMekugi2RepairContextForReversedRange(t *testing.T) {
	content := "alpha\nbeta\n"
	script := "type " + row(2, "beta") + ".." + row(1, "alpha") + ` ""`
	repair := repairFor(t, content, script)
	if !strings.Contains(repair, "row range resolves to lines 2:1") ||
		!strings.Contains(repair, "2:"+hashLine("beta")+" beta") {
		t.Fatalf("repair = %q", repair)
	}
}

func TestGeneratedSourceRepairIsBounded(t *testing.T) {
	long := strings.Repeat("x", repairPreviewLimit+50)
	content := strings.Join([]string{"one", "two", "three", "four", "five", long, "seven", "eight", "nine"}, "\n")
	repair := generatedSourceRepair(content, 6, 201)
	if strings.Count(repair, "\n") != 6 {
		t.Fatalf("repair line count = %d, want 6:\n%s", strings.Count(repair, "\n"), repair)
	}
	if !strings.Contains(repair, "generated Go near 6:201\n") ||
		!strings.Contains(repair, "> 6 | "+strings.Repeat("x", repairPreviewLimit)+"\n") ||
		strings.Contains(repair, long) || strings.Contains(repair, " 3 |") || strings.Contains(repair, " 9 |") {
		t.Fatalf("repair is not bounded around generated position:\n%s", repair)
	}
}
