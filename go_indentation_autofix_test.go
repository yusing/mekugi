package mekugi

import (
	"os"
	"strings"
	"testing"
)

func TestGoIndentationOnlyReplacementIsFormatted(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.go", "package p\n\nfunc f() {\n    return\n}\n", 0o644)

	edits := []FileEdit{{Path: "file.go", Script: "type " + row(4, "    return") + " \"  return\\n\""}}
	result, err := applyForHostAtTest(t, rootPath, edits, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got, want := readTestFile(t, rootPath, "file.go"), "package p\n\nfunc f() {\n\treturn\n}\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestNonGoIndentationOnlyReplacementRejectsWithoutSuggestion(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "script.sh", "header\n\texit \"$status\"\n", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	command := "type " + row(2, "\texit \"$status\"") + ` "exit \"$status\"\n"`
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, []FileEdit{{Path: "script.sh", Script: command}}, t.TempDir())
	if err == nil {
		t.Fatal("indentation-only replacement unexpectedly succeeded")
	}
	if !strings.Contains(result.Diagnostic, "indentation-only change to preserved text") {
		t.Fatalf("diagnostic = %q", result.Diagnostic)
	}
}

func TestIndentationCorrectionUsesEarliestCommandAcrossFiles(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "first.txt", "header\n\tfirst\n", 0o644)
	writeTestFile(t, rootPath, "second.sh", "header\n\tsecond\n", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	edits := []FileEdit{
		{Path: "first.txt", Script: ""},
		{Path: "second.sh", Script: "type " + row(2, "\tsecond") + ` "second\n"`},
		{Path: "first.txt", Script: "type " + row(2, "\tfirst") + ` "first\n"`},
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	if err == nil {
		t.Fatal("indentation-only replacements unexpectedly succeeded")
	}
	if len(result.Rejections) != 1 || result.Rejections[0].Command != 1 {
		t.Fatalf("rejections = %#v, want earliest command 1", result.Rejections)
	}
}

func TestPathResolutionPrecedesIndentationCorrection(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "script.sh", "header\n\texit\n", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	edits := []FileEdit{{Path: "script.sh", Script: "type " + row(2, "\texit") + ` "exit\n"`}, {Path: "missing.sh", Script: ""}}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	if err == nil {
		t.Fatal("path failure unexpectedly succeeded")
	}
	if len(result.Rejections) != 1 || result.Rejections[0].Command != 0 ||
		result.Rejections[0].Operation != "file" || result.Rejections[0].Reason != "file-path" ||
		result.Rejections[0].Path != "missing.sh" {
		t.Fatalf("rejections = %#v, want missing-path rejection", result.Rejections)
	}
}
