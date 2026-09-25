package router

import (
	"path/filepath"
	"testing"
)

func TestExecWindowStartExcludesEarlierClockTick(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unchanged")
	writeTestFile(t, path, "old")
	before, _, _, ok := execFileTimes(path)
	if !ok {
		t.Skip("file clock unavailable")
	}
	start := execWindowStart(t.TempDir())
	if start.IsZero() {
		t.Skip("file clock cannot advance within capture budget")
	}
	if !start.After(before) {
		t.Fatalf("window %v includes unchanged inode at %v", start, before)
	}
}
