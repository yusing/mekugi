package mekugi

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/patchtest"
)

func TestTextFramingThroughPublicOperations(t *testing.T) {
	const body = "type <<PATCH\nPATCH\ntype <<TEXT\n|body\nTEXT\n"
	const value = body
	for _, test := range []struct {
		name, script, want string
	}{
		{"initialize", "append <<END\n" + body + "END\n", value},
		{"literal", "type \"old\" <<'END'\n" + body + "END\n", value + "\nnext\n"},
		{"row", "type " + row(1, "old") + " <<END\n" + body + "END\n", value + "next\n"},
		{"range", "type " + row(1, "old") + ".." + row(2, "next") + " <<END\n" + body + "END\n", value},
		{"anchored", "type " + row(1, "old") + " \"old\" <<END\n" + body + "END\n", value + "\nnext\n"},
		{"insert", "add \"old\" <<END\n" + body + "END\n", value + "old\nnext\n"},
		{"append", "append <<END\n" + body + "END\n", "old\nnext\n" + value},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			before := map[string]string{"file.txt": "old\nnext\n"}
			if test.name == "initialize" {
				before["file.txt"] = ""
			}
			writeTestFile(t, root, "file.txt", before["file.txt"], 0o644)
			edits := []FileEdit{{Path: "file.txt", Script: test.script}}
			translated, err := translateForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatalf("translate: %v, %s", err, translated.Diagnostic)
			}
			tree, err := patchtest.Apply(before, string(translated.Patch))
			hostWant := test.want
			if hostWant != "" && !strings.HasSuffix(hostWant, "\n") {
				hostWant += "\n" // Native apply_patch terminates every resulting line.
			}
			if err != nil || tree["file.txt"] != hostWant {
				t.Fatalf("translated tree = %v, error %v; want %q", tree, err, hostWant)
			}
			result, err := applyForHostAtTest(t, root, edits, "")
			if err != nil || readTestFile(t, root, "file.txt") != test.want {
				t.Fatalf("apply: %v, %s; want %q", err, result.Diagnostic, test.want)
			}
		})
	}
}

func TestTextFrameSyntaxFailuresKeepValueRows(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.go", "", 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "append <<'GO'\npackage p\nvar =\nGO\n"}}
	result, err := translateForHostAtTest(t, root, edits, "")
	if err == nil || len(result.Rejections) != 1 || result.Rejections[0].Command != 1 || result.Rejections[0].SourceLine != 1 || result.Rejections[0].ValueLine != 2 {
		t.Fatalf("rejections = %+v, error %v; want command 1 value row 2", result.Rejections, err)
	}
	if len(result.Patch) != 0 || readTestFile(t, root, "file.go") != "" {
		t.Fatalf("invalid text value produced effects: %+v", result)
	}
}

func TestMalformedTextFrameRejectsWholeScript(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "old\n", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "type \"old\" \"prepared\"\nappend <<END\nfirst\nrm\nnew other.txt\ntype \"unintended\"\nWRONG\n"}}
	result, err := applyForHostAtTest(t, root, edits, "")
	if err == nil || !strings.Contains(result.Diagnostic, "unterminated heredoc") || strings.Count(result.Diagnostic, ": command") != 1 {
		t.Fatalf("expected one header-owned rejection: %v, %s", err, result.Diagnostic)
	}
	if tree := readTree(t, root); len(tree) != 1 || readTestFile(t, root, "file.txt") != "old\n" {
		t.Fatalf("rejection changed tree: %v", tree)
	}
}
