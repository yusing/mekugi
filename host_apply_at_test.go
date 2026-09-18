package mekugi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyForHostAtDirectoryAuthorityAndExactBytes(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"relative.txt", "../outside.txt", filepath.Join(parent, "absolute.txt")} {
		target := name
		if !filepath.IsAbs(target) {
			target = filepath.Join(base, target)
		}
		if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
			t.Fatal(err)
		}
		edits := []FileEdit{{Path: name, Script: `type "before" "a\r\nb"`}}
		result, err := ApplyForHostAt(t.Context(), base, edits, "")
		if err != nil || !result.Change.Applied {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "a\r\nb" {
			t.Fatalf("%s: %q %v", target, data, err)
		}
	}
	if _, err := ApplyForHostAt(t.Context(), "", []FileEdit{{Path: "relative.txt", Script: `type "before" "a"`}}, ""); err == nil || !strings.Contains(err.Error(), "relative path requires") {
		t.Fatalf("missing base: %v", err)
	}
}

func TestApplyForHostAtRejectsBeforeCommit(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, base, "first.txt", "first\n", 0o644)
	missing := []FileEdit{{Path: "first.txt", Script: `type "first\n" "new\n"`}, {Path: "missing.txt", Script: `type "old" "new"`}}
	if _, err := ApplyForHostAt(t.Context(), base, missing, ""); err == nil {
		t.Fatal("invalid edit succeeded")
	}
	if got := readTestFile(t, base, "first.txt"); got != "first\n" {
		t.Fatalf("partial edit: %q", got)
	}
	if _, err := ApplyForHostAt(nil, base, []FileEdit{{Path: "first.txt", Script: ""}}, ""); err == nil {
		t.Fatal("nil context accepted")
	}
}
