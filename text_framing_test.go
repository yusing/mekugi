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
		{"initialize", "new file.txt\ntype <<E'N'D\n" + body + "END\n", value},
		{"literal", "in file.txt\ntype \"old\" <<'END'\n" + body + "END\n", value + "\nnext\n"},
		{"row", "in file.txt\ntype " + row(1, "old") + " <<END\n" + body + "END\n", value + "next\n"},
		{"range", "in file.txt\ntype " + row(1, "old") + ".." + row(2, "next") + " <<END\n" + body + "END\n", value},
		{"anchored", "in file.txt\ntype " + row(1, "old") + " \"old\" <<END\n" + body + "END\n", value + "\nnext\n"},
		{"insert", "in file.txt\nadd \"old\" <<END\n" + body + "END\n", value + "old\nnext\n"},
		{"append", "in file.txt\nadd EOF <<END\n" + body + "END\n", "old\nnext\n" + value},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			before := map[string]string{}
			if test.name != "initialize" {
				before["file.txt"] = "old\nnext\n"
				writeTestFile(t, root, "file.txt", before["file.txt"], 0o644)
			}
			translated, err := translateForHostAtTest(t, root, test.script, "")
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
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err != nil || readTestFile(t, root, "file.txt") != test.want {
				t.Fatalf("apply: %v, %s; want %q", err, result.Diagnostic, test.want)
			}
		})
	}
}

func TestTextFrameSyntaxFailuresKeepValueRows(t *testing.T) {
	root := t.TempDir()
	script := "new file.go\ntype <<'GO'\npackage p\nvar =\nGO\n"
	result, err := translateForHostAtTest(t, root, script, "")
	if err == nil || len(result.Rejections) != 1 || result.Rejections[0].Command != 2 || result.Rejections[0].ValueLine != 2 {
		t.Fatalf("rejections = %+v, error %v; want command 2 value row 2", result.Rejections, err)
	}
	if len(result.Patch) != 0 || len(readTree(t, root)) != 0 {
		t.Fatalf("invalid text value produced effects: %+v", result)
	}
}

func TestMalformedTextFrameRejectsWholeScript(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "old\n", 0o644)
	script := "in file.txt\ntype \"old\" \"prepared\"\nadd EOF <<END\nfirst\nrm\nnew other.txt\ntype \"unintended\"\nWRONG\n"
	result, err := applyForHostAtTest(t, root, script, "")
	if err == nil || !strings.Contains(result.Diagnostic, "unterminated heredoc") || strings.Count(result.Diagnostic, ": command") != 1 {
		t.Fatalf("expected one header-owned rejection: %v, %s", err, result.Diagnostic)
	}
	if tree := readTree(t, root); len(tree) != 1 || readTestFile(t, root, "file.txt") != "old\n" {
		t.Fatalf("rejection changed tree: %v", tree)
	}
}
