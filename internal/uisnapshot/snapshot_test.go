package uisnapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotComparisonAndReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.txt")
	want := "│ expected  │\n\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	Assert(t, path, "\x1b[32m"+want+"\x1b[0m")
	for _, got := range []string{"│ changed   │\n\n", "│ expected │\n\n", "│ expected  │\n"} {
		err := check(path, got, false)
		if err == nil || !strings.Contains(err.Error(), "--- "+path) || !strings.Contains(err.Error(), "+++ "+path+".new") {
			t.Fatalf("mismatch did not produce a reviewable diff: %v", err)
		}
		assertFile(t, path, want)
		assertFile(t, path+".new", got)
	}
	if err := check(path, want, false); err != nil {
		t.Fatal(err)
	}
	assertNoCandidate(t, path)
}

func TestSnapshotMissingAndExplicitUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.txt")
	got := "│ new frame │\n"
	if err := check(path, got, false); err == nil {
		t.Fatal("missing baseline passed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("comparison created the baseline: %v", err)
	}
	assertFile(t, path+".new", got)
	t.Setenv("MEKUGI_UPDATE_UI_SNAPSHOTS", "1")
	Assert(t, path, "\x1b[32m"+got+"\x1b[0m")
	assertFile(t, path, got)
	assertNoCandidate(t, path)
	Assert(t, path, "updated frame\n")
	assertFile(t, path, "updated frame\n")
}

func TestSnapshotStorageErrors(t *testing.T) {
	dir := t.TempDir()
	if err := check(dir, "frame", false); err == nil || !strings.Contains(err.Error(), "read UI snapshot") {
		t.Fatalf("fixture read error was hidden: %v", err)
	}
	path := filepath.Join(dir, "missing", "frame.txt")
	for _, update := range []bool{false, true} {
		if err := check(path, "frame", update); err == nil {
			t.Fatalf("write error was hidden: update=%v", update)
		}
	}
}

func TestSnapshotTerminalCells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "styles.txt")
	rows := []string{"\x1b[1;31m猫é\x1b[0m", "plain"}
	want := terminalFrame(t, rows, 8)
	if err := check(path, want, true); err != nil {
		t.Fatal(err)
	}
	// Separate SGR commands and a short reset have the same terminal effect.
	equivalent := []string{"\x1b[31m\x1b[1m猫é\x1b[m", "plain"}
	AssertTerminal(t, path, equivalent, 8)
	for _, changed := range [][]string{
		{"\x1b[1;32m猫é\x1b[0m", "plain"},    // foreground only
		{"\x1b[1;31;44m猫é\x1b[0m", "plain"}, // background only
		{"\x1b[31m猫é\x1b[0m", "plain"},      // emphasis only
		{"\x1b[1;31m猫é", "plain"},           // missing reset leaks to the next row
		{"\x1b]8;;https://example.com\x1b\\\x1b[1;31m猫é\x1b[0m\x1b]8;;\x1b\\", "plain"},
	} {
		got := terminalFrame(t, changed, 8)
		if err := check(path, got, false); err == nil {
			t.Fatalf("terminal change passed: %q", changed)
		}
		assertFile(t, path, want)
		assertFile(t, path+".new", got)
	}
	// Quoting keeps controls inert and full-width padding reviewable.
	if strings.ContainsAny(want, "\x1b\r") || !strings.Contains(want, `"plain   "`) || !strings.Contains(want, "猫é") {
		t.Fatalf("terminal fixture lost quoting, padding, or Unicode: %q", want)
	}
	AssertTerminal(t, path, equivalent, 8)
	assertNoCandidate(t, path)
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s: got %q, want %q, error %v", path, got, want, err)
	}
}

func assertNoCandidate(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatalf("stale snapshot candidate remains: %v", err)
	}
}
