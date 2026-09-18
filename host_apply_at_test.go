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
		result, err := ApplyForHostAt(t.Context(), base, "new "+name+"\ntype \"a\\r\\nb\"", "")
		if err != nil || !result.Change.Applied {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		target := name
		if !filepath.IsAbs(target) {
			target = filepath.Join(base, target)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "a\r\nb" {
			t.Fatalf("%s: %q %v", target, data, err)
		}
	}
	if _, err := ApplyForHostAt(t.Context(), "", "new relative.txt", ""); err == nil || !strings.Contains(err.Error(), "relative path requires") {
		t.Fatalf("missing base: %v", err)
	}
}

func TestApplyForHostAtRejectsBeforeCommit(t *testing.T) {
	base := t.TempDir()
	if _, err := ApplyForHostAt(t.Context(), base, "new first.txt\ntype \"first\"\nin missing.txt\ntype \"old\" \"new\"", ""); err == nil {
		t.Fatal("invalid edit succeeded")
	}
	if _, err := os.Stat(filepath.Join(base, "first.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial edit: %v", err)
	}
	if _, err := ApplyForHostAt(nil, base, "", ""); err == nil {
		t.Fatal("nil context accepted")
	}
}
