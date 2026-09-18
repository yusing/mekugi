package mekugi

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/patchtest"
)

func TestMekugi2TranslateNormalizesCRLFDisplay(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "text.txt", "old\r\nkeep\r\n", 0o644)
	edits := []FileEdit{{Path: "text.txt", Script: "type " + row(1, "old") + ` "new"`}}
	result, err := translateForHostAtTest(t, root, edits, "")
	if err != nil {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if strings.Contains(string(result.Patch), "\r") || !strings.Contains(string(result.Patch), "-old\n+new\n keep\n") {
		t.Fatalf("translation does not describe LF logical-line edit:\n%s", string(result.Patch))
	}
	if got := readTestFile(t, root, "text.txt"); got != "old\r\nkeep\r\n" {
		t.Fatalf("translate mutated CRLF input: %q", got)
	}
}

func TestMekugi2TranslateDisambiguatesRepeatedBlocks(t *testing.T) {
	root := t.TempDir()
	content := "first\nrepeat\nvalue=old\nend\nmiddle\nrepeat\nvalue=old\nend\nlast\n"
	writeTestFile(t, root, "text.txt", content, 0o644)
	edits := []FileEdit{{Path: "text.txt", Script: "type " + row(5, "middle") + ` "old" "new"`}}
	result, err := translateForHostAtTest(t, root, edits, "")
	if err != nil {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	got, err := patchtest.Apply(map[string]string{"text.txt": content}, string(result.Patch))
	if err != nil {
		t.Fatalf("applying translation: %v\n%s", err, string(result.Patch))
	}
	want := map[string]string{"text.txt": "first\nrepeat\nvalue=old\nend\nmiddle\nrepeat\nvalue=new\nend\nlast\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tree = %#v, want %#v\n%s", got, want, string(result.Patch))
	}
}

func TestMekugi2QuotedOperandsAcceptLiteralTabs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "text.txt", "old\tvalue\n", 0o644)
	edits := []FileEdit{{Path: "text.txt", Script: "type " + row(1, "old\tvalue") + " \"old\tvalue\" \"new\tvalue\""}}
	result, err := applyForHostAtTest(t, root, edits, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "text.txt"); got != "new\tvalue\n" {
		t.Fatalf("text.txt = %q", got)
	}
}

func TestMekugi2FixedHeredocPreservesLiteralCRLFBody(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "append <<PATCH\none \"quoted\" \\ slash\tinside\ntwo\nPATCH\n"}}
	result, err := applyForHostAtTest(t, root, edits, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got, want := readTestFile(t, root, "file.txt"), "one \"quoted\" \\ slash\tinside\ntwo\n"; got != want {
		t.Fatalf("file.txt = %q, want %q", got, want)
	}
}

func TestMekugi2HeredocFailuresAreHeaderOwnedAndAtomic(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{name: "missing delimiter", script: "append <<\nraw\nBODY\n", want: "invalid heredoc delimiter"},
		{name: "mismatched delimiter", script: "append <<END\nraw\nOTHER\n", want: "unterminated heredoc"},
		{name: "unterminated quoted delimiter", script: "append <<'END'\nraw\n", want: "unterminated heredoc"},
		{name: "suffix is part of delimiter", script: "append <<PATCH-\nraw\nPATCH\n", want: "unterminated heredoc"},
		{name: "unterminated", script: "append <<PATCH\nraw\n", want: "unterminated heredoc"},
		{name: "oversized", script: "append <<PATCH\n" + strings.Repeat("x", hpatchsyntax.MaxHeredocBodyBytes+1) + "\nPATCH\n", want: "heredoc body exceeds"},
		{name: "invalid UTF-8", script: "append <<PATCH\n" + string([]byte{0xff}) + "\nPATCH\n", want: "heredoc body is not UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", "", 0o644)
			edits := []FileEdit{{Path: "file.txt", Script: test.script}}
			result, err := translateForHostAtTest(t, root, edits, "")
			if err == nil || !strings.Contains(result.Diagnostic, test.want) {
				t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if strings.Count(result.Diagnostic, ": command") != 1 || readTestFile(t, root, "file.txt") != "" {
				t.Fatalf("failure was not one header-owned atomic rejection: %q", result.Diagnostic)
			}
		})
	}
}

func TestMekugi2PhysicalNewlineInQuotedOperandIsHeaderOwned(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "original\n", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "type " + row(1, "original") + " \"replacement\ntext\"\n"}}
	result, err := translateForHostAtTest(t, root, edits, "")
	if err == nil ||
		strings.Count(result.Diagnostic, ": command") != 1 ||
		!strings.Contains(result.Diagnostic, `physical newline inside quoted operand; encode line terminators as \n or \r`) {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
}

func TestMekugi2ParserReportsIndependentSyntaxErrors(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "unchanged\n", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "type 1:0000 \"literal\x01control\"\ntype 0:0123 \"value\"\nunknown-command 1:abcd trailing\n"}}
	result, err := translateForHostAtTest(t, root, edits, "")
	if err == nil || strings.Count(result.Diagnostic, ": command") != 3 {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.txt"); got != "unchanged\n" {
		t.Fatalf("syntax rejection mutated file: %q", got)
	}
}

func TestMekugi2InvalidUTF8IsRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), []byte{0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostAtTest(t, root, []FileEdit{{Path: "binary.txt", Script: ""}}, "")
	if err == nil || !strings.Contains(result.Diagnostic, "not UTF-8") {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
}

func TestMekugi2NoopModeBoundaries(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "same\n", 0o644)
	applied, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: ""}}, "")
	if err != nil || !strings.HasPrefix(applied.Report, "file file.txt\nlast none\n") {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, applied.Report)
	}
	translated, err := translateForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: ""}}, "")
	if err != nil || len(translated.Patch) != 0 || !strings.HasPrefix(translated.Report, "file file.txt\nlast none\n") {
		t.Fatalf("translateForHostForTest() error = %v, patch %q, report %q", err, translated.Patch, translated.Report)
	}
}

func TestMekugi2AbsoluteNormalizedAndCWDPaths(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, rootPath, "bin/main.go", "package main\n", 0o644)
	root := openTestRoot(t, rootPath)
	workspace := Workspace{Root: root, CWD: "bin"}
	edits := []FileEdit{{Path: "./main.go", Script: "type " + row(1, "package main") + ` "package graph"`}}
	patch, err := translateForTest(t.Context(), workspace, edits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "*** Update File: bin/main.go\n") {
		t.Fatalf("translation is not root-relative:\n%s", patch)
	}
	if err := Apply(t.Context(), workspace, edits); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, rootPath, "bin/main.go"); got != "package graph\n" {
		t.Fatalf("main.go = %q", got)
	}
}

func TestMekugi2WorkspaceRejectsPathsOutsideRoot(t *testing.T) {
	rootPath := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, outside, "outside.txt", "old\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(rootPath, "escape")); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, rootPath)
	workspace := Workspace{Root: root}
	for _, path := range []string{"../outside.txt", filepath.Join(outside, "outside.txt"), "escape/outside.txt"} {
		if _, err := translateForTest(t.Context(), workspace, []FileEdit{{Path: path, Script: ""}}); err == nil {
			t.Fatalf("translateForTest(%q) succeeded", path)
		}
	}
	if err := Apply(t.Context(), workspace, []FileEdit{{Path: "escape/new.txt", Script: `append "bad"`}}); err == nil {
		t.Fatal("Apply() created a file through an escaping symlink")
	}
	if got := readTestFile(t, outside, "outside.txt"); got != "old\n" {
		t.Fatalf("outside file mutated: %q", got)
	}
}

func TestTranslateForHostAtWithoutDirectoryNeverUsesProcessCWD(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, directory, "existing.txt", "old\n", 0o644)
	t.Chdir(directory)

	absolute := filepath.Join(directory, "existing.txt")
	result, err := TranslateForHostAt(t.Context(), "", []FileEdit{{Path: absolute, Script: "type " + row(1, "old") + ` "new"`}}, t.TempDir())
	if err != nil {
		t.Fatalf("absolute host translation: %v", err)
	}
	if !bytes.Contains(result.Patch, []byte("*** Update File: "+absolute)) {
		t.Fatalf("absolute patch = %q", result.Patch)
	}

	if _, err := TranslateForHostAt(t.Context(), "", []FileEdit{{Path: "existing.txt", Script: ""}}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "relative path requires a host directory") {
		t.Fatalf("relative host translation error = %v", err)
	}
}

func openTestRoot(t *testing.T, path string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("closing root: %v", err)
		}
	})
	return root
}
