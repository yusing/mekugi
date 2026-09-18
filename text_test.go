package mekugi

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTextAppliesOrdinaryMutationsToImmutableBaseline(t *testing.T) {
	baseline := "one\ntwo\n"
	script := strings.Join([]string{
		`add ` + row(1, "one") + ` "zero\n"`,
		`type ` + row(2, "two") + ` "TWO\ntail\n"`,
	}, "\n")
	got, err := EditText(t.Context(), baseline, script)
	if err != nil {
		t.Fatal(err)
	}
	if want := "zero\none\nTWO\ntail\n"; got != want {
		t.Fatalf("EditText() = %q, want %q", got, want)
	}
}

func TestEditTextMutatesHeredocBodyByScriptRow(t *testing.T) {
	baseline := "type 1:ffff <<PATCH\nbad\nPATCH\n"
	got, err := EditText(t.Context(), baseline, `type `+row(2, "bad")+` "good"`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "type 1:ffff <<PATCH\ngood\nPATCH\n"; got != want {
		t.Fatalf("EditText() = %q, want %q", got, want)
	}
}

func TestEditTextRejectsCommandsOutsideMutationSubset(t *testing.T) {
	baseline := "type 1:ffff \"value\"\n"
	for _, script := range []string{"in other.txt", `type "initializer"`, "rm", `append "tail"`} {
		if got, err := EditText(t.Context(), baseline, script); err == nil || got != "" {
			t.Fatalf("EditText(%q) = %q, %v; want atomic rejection", script, got, err)
		}
	}
}

func TestTextReferencesUseTargetableRows(t *testing.T) {
	text := "first\r\nsecond\rthird\n"
	got := TextReferences(text, 2, 2, 4, 1, 3)
	want := "2:" + hashLine("second") + " second\n" +
		"1:" + hashLine("first") + " first\n" +
		"3:" + hashLine("third") + " third\n"
	if got != want {
		t.Fatalf("TextReferences() = %q, want %q", got, want)
	}
	if got := TextLineCount(text); got != 3 {
		t.Fatalf("TextLineCount() = %d, want 3", got)
	}
}

func TestGoSyntaxDiagnosticUsesConciseCommandShape(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "mekugi_invalid_recovery_probe.go", "", 0o644)
	edits := []FileEdit{{Path: "mekugi_invalid_recovery_probe.go", Script: "append <<PATCH\n" +
		"package main\n\n" +
		"func main() {\n" +
		"\tprintln(\"ok\")\n" +
		")\n" +
		"}\nPATCH\n"}}
	result, err := TranslateForHostAt(t.Context(), root, edits, t.TempDir())
	if err == nil {
		t.Fatal("invalid Go source unexpectedly succeeded")
	}
	want := fmt.Sprintf("append: command 1, path %q, reason language-syntax: expected statement, found ')' (and 1 more errors)\n", filepath.Join(root, "mekugi_invalid_recovery_probe.go"))
	if !strings.HasPrefix(result.Diagnostic, want) {
		t.Fatalf("diagnostic = %q, want prefix %q", result.Diagnostic, want)
	}
}

func TestEditTextBoundedChecksPlannedContent(t *testing.T) {
	for _, test := range []struct {
		name, baseline, script, want string
		limit                        int
		reject                       bool
	}{
		{"exact expanded size", "aa", `type "a" 2 "1234"`, "12341234", 8, false},
		{"expanded size", "aa", `type "a" 2 "1234"`, "", 7, true},
		{"owned terminator", "a\n", "type " + row(1, "a") + ` "long"`, "", 4, true},
		{"baseline", "long", `type "long" ""`, "", 3, true},
		{"intermediate size", "ab", "add \"a\" \"long\"\ntype \"ab\" \"\"", "", 3, true},
		{"empty zero", "", "", "", 0, false},
		{"negative limit", "", "", "", -1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := EditTextBounded(t.Context(), test.baseline, test.script, test.limit)
			if (err != nil) != test.reject || got != test.want {
				t.Fatalf("bounded edit = %q, %v; want %q, rejection %v", got, err, test.want, test.reject)
			}
		})
	}
}
