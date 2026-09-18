package mekugi

import (
	"strings"
	"testing"
)

func TestBoundaryAdvisoriesPreserveAuthoredBytes(t *testing.T) {
	for _, test := range []struct {
		name, before, edit, want, advisory string
	}{
		{"empty heredoc row", "old\nnext\n", "type " + row(1, "old") + " <<END\nEND\n", "next\n",
			"deletes=1 removes-ending=1"},
		{"quoted empty row", "old\nnext\n", "type " + row(1, "old") + " \"\"\n", "next\n",
			"deletes=1 removes-ending=1"},
		{"explicit deletion", "old\nnext\n", "type " + row(1, "old") + ` ""`, "next\n",
			"deletes=1 removes-ending=1"},
		{"row preservation", "old\nnext\n", "type " + row(1, "old") + ` "new"`, "new\nnext\n",
			""},
		{"range preservation", "old\r\nlast\r\nnext\r\n", "type " + row(1, "old") + ".." + row(2, "last") + ` "new"`, "new\r\nnext\r\n",
			""},
		{"CR preservation", "old\rnext\r", "type " + row(1, "old") + ` "new"`, "new\rnext\r",
			""},
		{"unterminated row", "old", "type " + row(1, "old") + " \"new\"\n", "new",
			""},
		{"literal newline removal", "old\nnext\n", `type "old\n" "new"`, "newnext\n",
			"removes-ending=1"},
		{"literal trailing newline", "old\nnext\n", "type \"old\" <<END\nnew\nEND\n", "new\n\nnext\n",
			"blank-after=1"},
		{"anchored literal", "old\nnext\n", "type " + row(1, "old") + " \"old\" <<END\nnew\nEND\n", "new\n\nnext\n",
			"blank-after=1"},
		{"insert before blank separator", "before\n \t\nnext\n", "add " + row(2, " \t") + ` "new\n"`, "before\nnew\n \t\nnext\n",
			"blank-after=1"},
		{"insert leading blank", "before\nnext\n", "add " + row(2, "next") + ` "\nnew\n"`, "before\n\nnew\nnext\n",
			"blank-before=1"},
		{"append joins unterminated line", "before", `append "after"`, "beforeafter",
			"joins-left=1"},
		{"multiple occurrences", "old\nold\n", `type "old" 2 "new\n"`, "new\n\nnew\n\n",
			"blank-after=2"},
		{"ordinary inline", "old\n", `type "old" "new"`, "new\n", ""},
		{"supplied row newline", "old\n", "type " + row(1, "old") + ` "new\n"`, "new\n", ""},
		{"unchanged row", "old\n", "type " + row(1, "old") + ` "old"`, "old\n", ""},
		{"split CRLF", "old\n", `type "old" "new\r"`, "new\r\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", test.before, 0o644)
			edits := []FileEdit{{Path: "file.txt", Script: test.edit}}
			translated, err := translateForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatalf("translate: %v, %s", err, translated.Diagnostic)
			}
			applied, err := applyForHostAtTest(t, root, edits, "")
			if err != nil || readTestFile(t, root, "file.txt") != test.want {
				t.Fatalf("apply: %v, %s; got %q, want %q", err, applied.Diagnostic, readTestFile(t, root, "file.txt"), test.want)
			}
			if translated.Report != applied.Report {
				t.Fatalf("apply/translate reports differ:\n%s\n%s", applied.Report, translated.Report)
			}
			if test.advisory == "" {
				if strings.Contains(applied.Report, "advisory ") {
					t.Fatalf("unexpected advisory: %s", applied.Report)
				}
			} else if !strings.Contains(applied.Report, ": "+test.advisory+"\n") || strings.Count(applied.Report, "advisory ") != 1 {
				t.Fatalf("report lacks one expected advisory %q:\n%s", test.advisory, applied.Report)
			}
		})
	}
}

func TestBoundaryAdvisoriesRemainBaselineEvidence(t *testing.T) {
	for _, test := range []struct {
		name, path, before, script, want, advisory string
	}{
		{
			name: "neighbor removes observed separator", path: "file.txt",
			before:   "old\nnext\n",
			script:   "type \"old\" \"new\\n\"\ntype \"\\n\" \"\"\n",
			want:     "new\nnext\n",
			advisory: "advisory 2: deletes=1 removes-ending=1\n",
		},
		{
			name: "formatter removes observed separator", path: "file.go",
			before:   "package p\n\nvar old=1\n",
			script:   "type \"var old=1\" \"var newer=2\\n\"\n",
			want:     "package p\n\nvar newer = 2\n",
			advisory: "advisory 1: blank-after=1\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, test.path, test.before, 0o644)
			edits := []FileEdit{{Path: test.path, Script: test.script}}
			result, err := applyForHostAtTest(t, root, edits, "")
			if err != nil || readTestFile(t, root, test.path) != test.want {
				t.Fatalf("apply: %v, %s; want %q", err, result.Diagnostic, test.want)
			}
			if !strings.Contains(result.Report, test.advisory) {
				t.Fatalf("lost authored baseline observation: %s", result.Report)
			}
		})
	}
}

func TestEmptyMultilineInitializerIsNotReportedAsDeletion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "append <<END\nEND\n"}}
	result, err := applyForHostAtTest(t, root, edits, "")
	if err != nil || readTestFile(t, root, "file.txt") != "" || strings.Contains(result.Report, "advisory ") {
		t.Fatalf("empty initializer: %v, %+v", err, result)
	}
}

func TestBlankBoundaryRecognizesTerminators(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        bool
	}{
		{"a\n", "\n", true}, {"a\r\n", " \t\r\n", true},
		{"a\r", "\r", true}, {"a\r", "\n", false},
		{"a\r", " \n", true}, {"a\n", " \tvalue\n", false},
		{"a", "\n", false}, {"a\n", "", false},
	} {
		if got := blankBoundary(test.left, test.right); got != test.want {
			t.Errorf("blankBoundary(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
		}
	}
}
